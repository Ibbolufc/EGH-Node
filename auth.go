package main

import (
    "errors"
    "net/http"
    "strings"

    jwt "github.com/golang-jwt/jwt/v5"
)

func requireDaemonAuth(cfg *Config, next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
        if authHeader == "" || !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
            http.Error(w, "missing bearer token", http.StatusUnauthorized)
            return
        }

        tokenString := strings.TrimSpace(authHeader[7:])
        if tokenString == "" {
            http.Error(w, "missing bearer token", http.StatusUnauthorized)
            return
        }

        secret := cfg.CurrentDaemonSecret()
        if secret == "" {
            http.Error(w, "daemon auth secret not configured", http.StatusForbidden)
            return
        }

        _, err := jwt.Parse(tokenString, func(token *jwt.Token) (any, error) {
            if token.Method == nil || token.Method.Alg() != jwt.SigningMethodHS256.Alg() {
                return nil, errors.New("unexpected signing method")
            }
            return []byte(secret), nil
        })
        if err != nil {
            http.Error(w, "invalid daemon token", http.StatusForbidden)
            return
        }

        next(w, r)
    }
}
