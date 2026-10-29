package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

type user struct {
	ID       int64  `json:"-"`
	Username string `json:"username"`
	HasImage bool   `json:"hasImage"`
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readCredentials(w http.ResponseWriter, r *http.Request) (credentials, bool) {
	var account credentials
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	err := decoder.Decode(&account)

	if err != nil{
		fail(w, http.StatusBadRequest, "invalid username or password")
		return account, false
	}

	//need to update err var or else i will get the "send single json object" err
	err = decoder.Decode(&struct{}{})

	if err != io.EOF {
		fail(w, http.StatusBadRequest, "send a single JSON object")
		return account, false
	}

	account.Username = strings.ToLower(strings.TrimSpace(account.Username))
	if account.Username == "" || account.Password == "" {
		fail(w, http.StatusBadRequest, "username and password are required")
		return account, false
	}

	if len(account.Password) > 72 {
		fail(w, http.StatusBadRequest, "password is too long")
		return account, false
	}

	if len(account.Username) > 50 {
		fail(w, http.StatusBadRequest, "your username is way to long")
		return account, false
	}

	return account, true
}

func (a *app) signup(w http.ResponseWriter, r *http.Request) {
	account, ok := readCredentials(w, r)
	if !ok {
		return
	}

	passwordHash, err := bcrypt.GenerateFromPassword(
		[]byte(account.Password),
		bcrypt.DefaultCost,
	)

	if err != nil {
		serverError(w, err)
		return
	}

	var newUser user
	err = a.db.QueryRowContext(
		r.Context(),
		`INSERT INTO users (username, password_hash)
		 VALUES ($1, $2)
		 RETURNING id, username`,
		account.Username,
		string(passwordHash),
	).Scan(&newUser.ID, &newUser.Username)

	var postgresError *pq.Error
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		fail(w, http.StatusConflict, "that username is already taken")
		return
	}

	if err != nil {
		serverError(w, err)
		return
	}

	if err := a.startSession(w, r, newUser.ID); err != nil {
		serverError(w, err)
		return
	}

	jsonResponse(w, http.StatusCreated, newUser)
}

func (a *app) login(w http.ResponseWriter, r *http.Request) {
	account, ok := readCredentials(w, r)
	if !ok {
		return
	}

	var existingUser user
	var passwordHash string

	err := a.db.QueryRowContext(
		r.Context(),
		`SELECT id, username, has_image, password_hash
		 FROM users WHERE username = $1`,
		account.Username,
	).Scan(
		&existingUser.ID,
		&existingUser.Username,
		&existingUser.HasImage,
		&passwordHash,
	)

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		serverError(w, err)
		return
	}

	if errors.Is(err, sql.ErrNoRows) ||
		bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(account.Password)) != nil {
		fail(w, http.StatusUnauthorized, "incorrect username or password")
		return
	}

	if err := a.startSession(w, r, existingUser.ID); err != nil {
		serverError(w, err)
		return
	}

	jsonResponse(w, http.StatusOK, existingUser)
}

func (a *app) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return err
	}

	sessionToken := hex.EncodeToString(token)
	expiresAt := time.Now().Add(24 * time.Hour)

	_, err := a.db.ExecContext(
		r.Context(),
		"DELETE FROM sessions WHERE expires_at <= NOW()",
	)
	if err != nil {
		return err
	}

	_, err = a.db.ExecContext(
		r.Context(),
		`INSERT INTO sessions (token_hash, user_id, expires_at)
		 VALUES ($1, $2, $3)`,
		tokenHash(sessionToken),
		userID,
		expiresAt,
	)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		Expires:  expiresAt,
		MaxAge:   86400,
	})

	return nil
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func (a *app) currentUser(w http.ResponseWriter, r *http.Request) (user, bool) {
	var currentUser user
	cookie, err := r.Cookie("session")

	if err != nil || len(cookie.Value) != 64 {
		fail(w, http.StatusUnauthorized, "please sign in")
		return currentUser, false
	}

	err = a.db.QueryRowContext(
		r.Context(),
		`SELECT users.id, users.username, users.has_image
		 FROM users
		 JOIN sessions ON sessions.user_id = users.id
		 WHERE sessions.token_hash = $1 AND sessions.expires_at > NOW()`,
		tokenHash(cookie.Value),
	).Scan(
		&currentUser.ID,
		&currentUser.Username,
		&currentUser.HasImage,
	)

	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusUnauthorized, "please sign in again")
		return currentUser, false
	}

	if err != nil {
		serverError(w, err)
		return currentUser, false
	}

	return currentUser, true
}

func (a *app) me(w http.ResponseWriter, r *http.Request) {
	currentUser, ok := a.currentUser(w, r)
	if !ok {
		return
	}

	jsonResponse(w, http.StatusOK, currentUser)
}

func (a *app) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session")

	if err == nil {
		_, err = a.db.ExecContext(
			r.Context(),
			"DELETE FROM sessions WHERE token_hash = $1",
			tokenHash(cookie.Value),
		)

		if err != nil {
			serverError(w, err)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
	})

	w.WriteHeader(http.StatusNoContent)
}
