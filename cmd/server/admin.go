package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func adminOnly(adminToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(adminToken) == "" {
			http.Error(w, "admin token not configured", http.StatusServiceUnavailable)
			return
		}
		if !validAdminBearer(r.Header.Get("Authorization"), adminToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="rfd-discord-bot"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func swordswallowerOnly(adminToken, listenerSecret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(adminToken) == "" && strings.TrimSpace(listenerSecret) == "" {
			http.Error(w, "swordswallower token not configured", http.StatusServiceUnavailable)
			return
		}
		auth := r.Header.Get("Authorization")
		secret := r.Header.Get("X-Swordswallower-Secret")
		if validAdminBearer(auth, adminToken) ||
			validAdminBearer(auth, listenerSecret) ||
			validSharedSecret(secret, listenerSecret) ||
			validSharedSecret(secret, adminToken) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="rfd-discord-bot"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func validAdminBearer(header, adminToken string) bool {
	adminToken = strings.TrimSpace(adminToken)
	if adminToken == "" {
		return false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	supplied := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if len(supplied) != len(adminToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(adminToken)) == 1
}

func validSharedSecret(header, expected string) bool {
	expected = strings.TrimSpace(expected)
	supplied := strings.TrimSpace(header)
	if expected == "" || supplied == "" || len(supplied) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1
}
