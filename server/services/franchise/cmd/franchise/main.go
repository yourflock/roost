// main.go — Roost Franchise Mode Service.
// Allows operators to white-label Roost under their own brand and subdomain,
// create subscription plans for their subscribers, and receive payouts via
// Stripe Connect Express.
//
// Port: 8114 (env: FRANCHISE_PORT). Internal + admin service.
//
// Routes (admin — require X-Admin-Key header):
//   POST /franchise/operators              — create operator account
//   GET  /franchise/operators              — list all operators
//   GET  /franchise/operators/{id}         — get operator details
//   PUT  /franchise/operators/{id}         — update operator config
//   POST /franchise/operators/{id}/suspend — suspend operator
//   POST /franchise/operators/{id}/activate — activate operator
//   GET  /franchise/operators/{id}/connect — start Stripe Connect onboarding
//
// Routes (operator — require X-Operator-ID + X-User-ID headers):
//   POST /franchise/subscriptions          — subscribe a user to an operator plan
//   GET  /franchise/subscriptions          — list operator's subscribers
//   GET  /franchise/stats                  — operator revenue/subscriber stats
//
//   GET  /health
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
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

// requireAdminKey checks X-Admin-Key header against FRANCHISE_ADMIN_KEY env var.
func requireAdminKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adminKey := getEnv("FRANCHISE_ADMIN_KEY", "")
		if adminKey == "" || r.Header.Get("X-Admin-Key") != adminKey {
			writeError(w, http.StatusUnauthorized, "unauthorized", "valid X-Admin-Key required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireOperator checks X-Operator-ID and X-User-ID headers.
func requireOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Operator-ID") == "" || r.Header.Get("X-User-ID") == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "X-Operator-ID and X-User-ID headers required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── Stripe Connect helpers ───────────────────────────────────────────────────

// stripePost makes a form-encoded POST to Stripe API v1.
func stripePost(path string, params url.Values) (map[string]interface{}, error) {
	stripeKey := getEnv("STRIPE_SECRET_KEY", "")
	if stripeKey == "" {
		return nil, fmt.Errorf("STRIPE_SECRET_KEY not configured")
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.stripe.com/v1"+path,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+stripeKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("stripe response parse error: %v", err)
	}
	if resp.StatusCode >= 400 {
		msg := "stripe error"
		if errObj, ok := result["error"].(map[string]interface{}); ok {
			if m, ok := errObj["message"].(string); ok {
				msg = m
			}
		}
		return nil, fmt.Errorf("stripe %d: %s", resp.StatusCode, msg)
	}
	return result, nil
}

// createStripeConnectAccount creates a Stripe Connect Express account for an operator.
func createStripeConnectAccount(operatorName, email string) (string, string, error) {
	params := url.Values{
		"type":         {"express"},
		"business_type": {"individual"},
		"capabilities[card_payments][requested]": {"true"},
		"capabilities[transfers][requested]":     {"true"},
		"business_profile[name]": {operatorName},
	}
	if email != "" {
		params.Set("email", email)
	}

	result, err := stripePost("/accounts", params)
	if err != nil {
		return "", "", err
	}
	accountID, _ := result["id"].(string)

	// Generate onboarding link.
	linkParams := url.Values{
		"account":     {accountID},
		"refresh_url": {getEnv("FRANCHISE_STRIPE_REFRESH_URL", "https://roost.yourflock.org/franchise/connect/refresh")},
		"return_url":  {getEnv("FRANCHISE_STRIPE_RETURN_URL", "https://roost.yourflock.org/franchise/connect/complete")},
		"type":        {"account_onboarding"},
	}
	linkResult, err := stripePost("/account_links", linkParams)
	if err != nil {
		return accountID, "", err
	}
	onboardURL, _ := linkResult["url"].(string)
	return accountID, onboardURL, nil
}

// ─── models ──────────────────────────────────────────────────────────────────

type Operator struct {
	ID              string          `json:"id"`
	OperatorName    string          `json:"operator_name"`
	OwnerUserID     string          `json:"owner_user_id"`
	StripeAccountID string          `json:"stripe_account_id,omitempty"`
	Subdomain       string          `json:"subdomain"`
	Config          json.RawMessage `json:"config"`
	Status          string          `json:"status"`
	CreatedAt       string          `json:"created_at"`
}

type Subscription struct {
	ID               string `json:"id"`
	OperatorID       string `json:"operator_id"`
	SubscriberUserID string `json:"subscriber_user_id"`
	PlanID           string `json:"plan_id"`
	StripeSubID      string `json:"stripe_sub_id,omitempty"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
}

// ─── server ──────────────────────────────────────────────────────────────────

type server struct{ db *sql.DB }

// ─── admin handlers ───────────────────────────────────────────────────────────

func (s *server) handleCreateOperator(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OperatorName string          `json:"operator_name"`
		OwnerUserID  string          `json:"owner_user_id"`
		Subdomain    string          `json:"subdomain"`
		Email        string          `json:"email"`
		Config       json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.OperatorName == "" || body.Subdomain == "" || body.OwnerUserID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "operator_name, subdomain, and owner_user_id are required")
		return
	}
	if body.Config == nil {
		body.Config = json.RawMessage(`{}`)
	}

	// Create Stripe Connect Express account.
	stripeAccountID, onboardURL, stripeErr := createStripeConnectAccount(body.OperatorName, body.Email)
	if stripeErr != nil {
		log.Printf("[franchise] stripe connect error: %v", stripeErr)
		// Don't block creation — operator can connect Stripe later.
	}

	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO franchise_operators (operator_name, owner_user_id, stripe_account_id, subdomain, config, status)
		 VALUES ($1, $2, $3, $4, $5::jsonb, 'pending') RETURNING id`,
		body.OperatorName, body.OwnerUserID,
		nullStr(stripeAccountID), body.Subdomain, string(body.Config),
	).Scan(&id)
	if err != nil {
		log.Printf("[franchise] db insert error: %v", err)
		if strings.Contains(err.Error(), "duplicate") {
			writeError(w, http.StatusConflict, "conflict", "subdomain already taken")
			return
		}
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create operator")
		return
	}

	resp := map[string]string{"id": id, "status": "pending"}
	if onboardURL != "" {
		resp["stripe_onboard_url"] = onboardURL
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *server) handleListOperators(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, operator_name, owner_user_id, COALESCE(stripe_account_id,''),
		        subdomain, config, status, created_at::text
		 FROM franchise_operators ORDER BY created_at DESC LIMIT 200`,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	operators := []Operator{}
	for rows.Next() {
		var op Operator
		var configRaw []byte
		if err := rows.Scan(&op.ID, &op.OperatorName, &op.OwnerUserID,
			&op.StripeAccountID, &op.Subdomain, &configRaw, &op.Status, &op.CreatedAt); err != nil {
			continue
		}
		op.Config = json.RawMessage(configRaw)
		operators = append(operators, op)
	}
	writeJSON(w, http.StatusOK, operators)
}

func (s *server) handleGetOperator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var op Operator
	var configRaw []byte
	err := s.db.QueryRowContext(r.Context(),
		`SELECT id, operator_name, owner_user_id, COALESCE(stripe_account_id,''),
		        subdomain, config, status, created_at::text
		 FROM franchise_operators WHERE id = $1`,
		id,
	).Scan(&op.ID, &op.OperatorName, &op.OwnerUserID, &op.StripeAccountID,
		&op.Subdomain, &configRaw, &op.Status, &op.CreatedAt)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	op.Config = json.RawMessage(configRaw)
	writeJSON(w, http.StatusOK, op)
}

func (s *server) handleUpdateOperator(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var body struct {
		OperatorName string          `json:"operator_name"`
		Config       json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}

	configJSON := `{}`
	if body.Config != nil {
		configJSON = string(body.Config)
	}

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE franchise_operators
		 SET operator_name = COALESCE(NULLIF($1,''), operator_name),
		     config = $2::jsonb
		 WHERE id = $3`,
		body.OperatorName, configJSON, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) handleSuspend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.setOperatorStatus(w, r, id, "suspended")
}

func (s *server) handleActivate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.setOperatorStatus(w, r, id, "active")
}

func (s *server) setOperatorStatus(w http.ResponseWriter, r *http.Request, id, status string) {
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE franchise_operators SET status = $1 WHERE id = $2`,
		status, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

// handleConnect generates a new Stripe Connect onboarding link for an existing operator.
func (s *server) handleConnect(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var stripeAccountID sql.NullString
	err := s.db.QueryRowContext(r.Context(),
		`SELECT stripe_account_id FROM franchise_operators WHERE id = $1`,
		id,
	).Scan(&stripeAccountID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "operator not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}

	if !stripeAccountID.Valid || stripeAccountID.String == "" {
		writeError(w, http.StatusBadRequest, "no_stripe_account", "operator has no Stripe account; create one first")
		return
	}

	linkParams := url.Values{
		"account":     {stripeAccountID.String},
		"refresh_url": {getEnv("FRANCHISE_STRIPE_REFRESH_URL", "https://roost.yourflock.org/franchise/connect/refresh")},
		"return_url":  {getEnv("FRANCHISE_STRIPE_RETURN_URL", "https://roost.yourflock.org/franchise/connect/complete")},
		"type":        {"account_onboarding"},
	}
	result, err := stripePost("/account_links", linkParams)
	if err != nil {
		log.Printf("[franchise] stripe account_links error: %v", err)
		writeError(w, http.StatusInternalServerError, "stripe_error", err.Error())
		return
	}
	onboardURL, _ := result["url"].(string)
	writeJSON(w, http.StatusOK, map[string]string{"url": onboardURL})
}

// ─── operator handlers ────────────────────────────────────────────────────────

func (s *server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	operatorID := r.Header.Get("X-Operator-ID")

	var body struct {
		SubscriberUserID string `json:"subscriber_user_id"`
		PlanID           string `json:"plan_id"`
		StripeSubID      string `json:"stripe_sub_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if body.SubscriberUserID == "" || body.PlanID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "subscriber_user_id and plan_id are required")
		return
	}

	var id string
	err := s.db.QueryRowContext(r.Context(),
		`INSERT INTO franchise_subscriptions (operator_id, subscriber_user_id, plan_id, stripe_sub_id, status)
		 VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
		operatorID, body.SubscriberUserID, body.PlanID, nullStr(body.StripeSubID),
	).Scan(&id)
	if err != nil {
		log.Printf("[franchise] subscription insert error: %v", err)
		writeError(w, http.StatusInternalServerError, "db_error", "failed to create subscription")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "active"})
}

func (s *server) handleListSubscriptions(w http.ResponseWriter, r *http.Request) {
	operatorID := r.Header.Get("X-Operator-ID")

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, operator_id, subscriber_user_id, plan_id,
		        COALESCE(stripe_sub_id,''), status, created_at::text
		 FROM franchise_subscriptions WHERE operator_id = $1
		 ORDER BY created_at DESC LIMIT 200`,
		operatorID,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	defer rows.Close()

	subs := []Subscription{}
	for rows.Next() {
		var sub Subscription
		if err := rows.Scan(&sub.ID, &sub.OperatorID, &sub.SubscriberUserID,
			&sub.PlanID, &sub.StripeSubID, &sub.Status, &sub.CreatedAt); err != nil {
			continue
		}
		subs = append(subs, sub)
	}
	writeJSON(w, http.StatusOK, subs)
}

func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	operatorID := r.Header.Get("X-Operator-ID")

	var total, active, cancelled int
	err := s.db.QueryRowContext(r.Context(),
		`SELECT
		   COUNT(*) AS total,
		   COUNT(*) FILTER (WHERE status = 'active') AS active,
		   COUNT(*) FILTER (WHERE status = 'cancelled') AS cancelled
		 FROM franchise_subscriptions WHERE operator_id = $1`,
		operatorID,
	).Scan(&total, &active, &cancelled)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"total_subscribers":     total,
		"active_subscribers":    active,
		"cancelled_subscribers": cancelled,
	})
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "roost-franchise"})
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	db, err := connectDB()
	if err != nil {
		log.Fatalf("[franchise] database connection failed: %v", err)
	}
	defer db.Close()

	srv := &server{db: db}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/health", srv.handleHealth)

	// Admin routes (require admin key)
	r.Group(func(r chi.Router) {
		r.Use(requireAdminKey)
		r.Post("/franchise/operators", srv.handleCreateOperator)
		r.Get("/franchise/operators", srv.handleListOperators)
		r.Get("/franchise/operators/{id}", srv.handleGetOperator)
		r.Put("/franchise/operators/{id}", srv.handleUpdateOperator)
		r.Post("/franchise/operators/{id}/suspend", srv.handleSuspend)
		r.Post("/franchise/operators/{id}/activate", srv.handleActivate)
		r.Get("/franchise/operators/{id}/connect", srv.handleConnect)
	})

	// Operator routes (require operator ID)
	r.Group(func(r chi.Router) {
		r.Use(requireOperator)
		r.Post("/franchise/subscriptions", srv.handleCreateSubscription)
		r.Get("/franchise/subscriptions", srv.handleListSubscriptions)
		r.Get("/franchise/stats", srv.handleStats)
	})

	port := getEnv("FRANCHISE_PORT", "8114")
	addr := ":" + port
	log.Printf("[franchise] starting on %s", addr)

	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatalf("[franchise] server error: %v", err)
	}
}

// _ avoids "imported and not used" if uuid is only used via uuid.New().String() in tests
var _ = uuid.New
