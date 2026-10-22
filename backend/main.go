
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id BIGSERIAL PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	has_image BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions (expires_at);
`

type app struct {
	db     *sql.DB
	s3     *s3.Client
	bucket string
}

func main() {
	err := run()
	if err != nil {
		log.Fatal(err)
	}
}

func run() error {
	err := godotenv.Load("../.env")
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	logPath := env("LOG_PATH", "../data/app.log")
	err = os.MkdirAll(filepath.Dir(logPath), 0755)
	if err != nil {
		return err
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer logFile.Close()

	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	databaseURL := url.URL{
		Scheme: "postgres",
		User: url.UserPassword(
			os.Getenv("DB_USER"),
			os.Getenv("DB_PASSWORD"),
		),
		Host: net.JoinHostPort(
			os.Getenv("DB_HOST"),
			os.Getenv("DB_PORT"),
		),
		Path:     "/" + os.Getenv("DB_NAME"),
		RawQuery: "sslmode=disable",
	}

	db, err := sql.Open("postgres", databaseURL.String())
	if err != nil {
		return err
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err = db.PingContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}

	_, err = db.ExecContext(ctx, schema)
	if err != nil {
		return fmt.Errorf("create tables: %w", err)
	}

	//connect to S3
	awsConfig, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return err
	}

	s3Client := s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.UsePathStyle = true
	})

	a := &app{
		db:     db,
		s3:     s3Client,
		bucket: env("S3_BUCKET", "floci-demo-images"),
	}

	server := &http.Server{
		Addr:              ":" + env("BACKEND_PORT", "8080"),
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctxStop, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	defer stop()
	go func() {
		<-ctxStop.Done()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := server.Shutdown(ctx)
		if err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("listening on %s", server.Addr)
	err = server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}

	return nil
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", a.health)
	mux.HandleFunc("POST /api/signup", a.signup)
	mux.HandleFunc("POST /api/login", a.login)
	mux.HandleFunc("POST /api/logout", a.logout)
	mux.HandleFunc("GET /api/me", a.me)
	mux.HandleFunc("POST /api/image", a.uploadImage)
	mux.HandleFunc("GET /api/image", a.getImage)

	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "API route not found")
	})

	// serve the built frontend in production
	frontend := env("FRONTEND_DIST", "../frontend/dist")
	mux.Handle("GET /", http.FileServer(http.Dir(frontend)))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")

		// reject cross-origin requests that modify data
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			origin := r.Header.Get("Origin")
			expectedOrigin := scheme + "://" + r.Host

			if origin != "" && origin != expectedOrigin {
				fail(w, http.StatusForbidden, "request must come from this app")
				return
			}

			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				fail(w, http.StatusForbidden, "request must come from this app")
				return
			}
		}

		mux.ServeHTTP(w, r)
	})
}

func (a *app) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	err := a.db.PingContext(ctx)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

func env(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	err := json.NewEncoder(w).Encode(value)
	if err != nil {
		log.Printf("write response: %v", err)
	}
}

func fail(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{
		"error": message,
	})
}

func serverError(w http.ResponseWriter, err error) {
	log.Printf("request failed: %v", err)
	fail(w, http.StatusInternalServerError, "something went wrong; please try again")
}

