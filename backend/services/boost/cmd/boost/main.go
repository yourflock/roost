// main.go — Roost Boost Storage Service.
// Handles family photo uploads to R2, face cluster management, and async
// photo organisation. Face detection is async — upload returns immediately;
// a background goroutine marks processed photos once grouping is complete.
//
// Port: 8110 (env: BOOST_PORT). Internal service — called by flock backend.
//
// Routes:
//   POST /boost/upload            — multipart photo upload → R2 + DB record
//   GET  /boost/photos            — list family photos (X-Family-ID header)
//   GET  /boost/clusters          — list face clusters for family
//   PUT  /boost/clusters/{id}     — label or assign member to cluster
//   POST /boost/organize          — trigger async face grouping job
//   GET  /health
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func connectDB() (*sql.DB, error) {
	dsn := getEnv("POSTGRES_URL", "postgres://roost:roost@localhost:5433/roost_dev?sslmode=disable")
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return db, db.PingContext(ctx)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]string{"error": code, "message": msg})
}

// requireFamilyAuth rejects requests missing X-Family-ID or X-User-ID headers.
func requireFamilyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Family-ID") == "" || r.Header.Get("X-User-ID") == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "X-Family-ID and X-User-ID headers required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// uploadToR2 PUTs an object to Cloudflare R2 using HMAC-signed auth.
func uploadToR2(r2Key string, data []byte, contentType string) error {
	endpoint := getEnv("R2_ENDPOINT", "https://r2.yourflock.org")
	bucket := getEnv("R2_BUCKET", "flock-media")
	accessKey := getEnv("R2_ACCESS_KEY_ID", "")
	secretKey := getEnv("R2_SECRET_ACCESS_KEY", "")

	url := fmt.Sprintf("%s/%s/%s", endpoint, bucket, r2Key)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("r2 build request: %w", err)
	}

	date := time.Now().UTC().Format("20060102")
	mac := hmac.New(sha256.New, []byte(secretKey))
	mac.Write([]byte(fmt.Sprintf("%s:%s:%s", accessKey, date, r2Key)))
	sig := hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("Authorization", fmt.Sprintf("ROOST-HMAC %s:%s", accessKey, sig))
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(data))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("r2 do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("r2 upload failed status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func fileExtFromHeader(h *multipart.FileHeader) string {
	switch h.Header.Get("Content-Type") {
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".jpg"
	}
}

func nullableString(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── handlers ────────────────────────────────────────────────────────────────

// handleUploadPhoto receives a multipart photo, stores it in R2, and records
// the upload in boost_uploads with status 'pending' for later face processing.
func (s *server) handleUploadPhoto(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	uploaderID := r.Header.Get("X-User-ID")

	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "failed to parse multipart form")
		return
	}
	file, header, err := r.FormFile("photo")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_field", "photo field required")
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_error", "failed to read file")
		return
	}

	photoID := uuid.New().String()
	ext := fileExtFromHeader(header)
	r2Key := fmt.Sprintf("boost/%s/%s%s", familyID, photoID, ext)
	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}

	if err := uploadToR2(r2Key, data, contentType); err != nil {
		log.Printf("[boost] r2 upload error: %v", err)
		writeError(w, http.StatusInternalServerError, "upload_failed", "photo upload to R2 failed")
		return
	}

	eventLabel := r.FormValue("event_label")
	uploadDate := r.FormValue("upload_date")
	if uploadDate == "" {
		uploadDate = time.Now().Format("2006-01-02")
	}

	var id string
	err = s.db.QueryRowContext(r.Context(),
		`INSERT INTO boost_uploads (family_id, uploader_id, file_key, event_label, upload_date, r2_key, status)
		 VALUES ($1, $2, $3, $4, $5::date, $6, 'pending') RETURNING id`,
		familyID, uploaderID, photoID,
		nullableString(eventLabel), uploadDate, r2Key,
	).Scan(&id)
	if err != nil {
		log.Printf("[boost] db insert error: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to record upload")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":     id,
		"r2_key": r2Key,
		"status": "pending",
	})
}

type photo struct {
	ID         string `json:"id"`
	FileKey    string `json:"file_key"`
	R2Key      string `json:"r2_key"`
	EventLabel string `json:"event_label"`
	UploadDate string `json:"upload_date"`
	FaceCount  int    `json:"face_count"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
}

func (s *server) handleListPhotos(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, file_key, r2_key, COALESCE(event_label,''), COALESCE(upload_date::text,''),
		        face_count, status, created_at::text
		 FROM boost_uploads
		 WHERE family_id = $1 AND status != 'deleted'
		 ORDER BY created_at DESC LIMIT 200`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	photos := []photo{}
	for rows.Next() {
		var p photo
		if err := rows.Scan(&p.ID, &p.FileKey, &p.R2Key, &p.EventLabel, &p.UploadDate,
			&p.FaceCount, &p.Status, &p.CreatedAt); err != nil {
			continue
		}
		photos = append(photos, p)
	}
	writeJSON(w, http.StatusOK, photos)
}

type cluster struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	MemberID string `json:"member_id,omitempty"`
}

func (s *server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, label, COALESCE(member_id::text, '')
		 FROM boost_face_clusters
		 WHERE family_id = $1
		 ORDER BY label ASC`,
		familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	clusters := []cluster{}
	for rows.Next() {
		var c cluster
		if err := rows.Scan(&c.ID, &c.Label, &c.MemberID); err != nil {
			continue
		}
		clusters = append(clusters, c)
	}
	writeJSON(w, http.StatusOK, clusters)
}

func (s *server) handleLabelCluster(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	clusterID := chi.URLParam(r, "id")

	var body struct {
		Label    string `json:"label"`
		MemberID string `json:"member_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.Label == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "label is required")
		return
	}

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE boost_face_clusters SET label = $1, member_id = $2
		 WHERE id = $3 AND family_id = $4`,
		body.Label, nullableString(body.MemberID), clusterID, familyID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "cluster not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleOrganize queues an async face-grouping job. In production this would
// dispatch to a worker queue; here it runs in a goroutine and marks uploads as
// processed once done.
func (s *server) handleOrganize(w http.ResponseWriter, r *http.Request) {
	familyID := r.Header.Get("X-Family-ID")
	jobID := uuid.New().String()

	go func() {
		log.Printf("[boost] organize job %s: starting for family %s", jobID, familyID)
		// Face detection placeholder: in production, dispatch to pigo/face-detection worker,
		// group face embeddings by cosine similarity, write clusters to boost_face_clusters,
		// and write photo→cluster mappings to boost_photo_faces.
		time.Sleep(200 * time.Millisecond)
		ctx := context.Background()
		if _, err := s.db.ExecContext(ctx,
			`UPDATE boost_uploads SET status = 'processed' WHERE family_id = $1 AND status = 'pending'`,
			familyID,
		); err != nil {
			log.Printf("[boost] organize job %s: db update error: %v", jobID, err)
			return
		}
		log.Printf("[boost] organize job %s: complete", jobID)
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id": jobID,
		"status": "queued",
	})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-boost"})
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[boost] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/health", srv.handleHealth)

	r.Group(func(r chi.Router) {
		r.Use(requireFamilyAuth)
		r.Post("/boost/upload", srv.handleUploadPhoto)
		r.Get("/boost/photos", srv.handleListPhotos)
		r.Get("/boost/clusters", srv.handleListClusters)
		r.Put("/boost/clusters/{id}", srv.handleLabelCluster)
		r.Post("/boost/organize", srv.handleOrganize)
	})

	port := getEnv("BOOST_PORT", "8110")
	addr := ":" + port
	log.Printf("[boost] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[boost] server error: %v", err)
	}
}
