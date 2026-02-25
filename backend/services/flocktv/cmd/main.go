// main.go — Flock TV service entrypoint.
// Phase FLOCKTV: starts the FlockTV HTTP service on FLOCKTV_PORT (default 8105).
// Requires DATABASE_URL pointing to the Roost primary Postgres instance.
package main

import (
	"log"
	"os"

	flocktv "github.com/yourflock/roost/services/flocktv"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is required")
	}

	db, err := flocktv.ConnectDB(dsn)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	defer db.Close()

	srv := flocktv.NewServer(db)
	if err := srv.Run(); err != nil {
		log.Fatalf("flocktv service error: %v", err)
	}
}
