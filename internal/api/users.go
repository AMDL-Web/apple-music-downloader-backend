package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"amdl/internal/auth"
	"amdl/internal/db"
	"amdl/internal/domain"
)

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	if identity.UserID == "" {
		// Single-user mode: the built-in admin has no stored profile.
		writeJSON(w, http.StatusOK, map[string]any{"user_id": "", "username": "", "role": domain.RoleAdmin, "avatar_url": ""})
		return
	}
	user, err := s.store.GetUser(r.Context(), identity.UserID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": user.ID, "username": user.Username, "role": user.Role, "avatar_url": user.AvatarURL})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type createUserRequest struct {
	Username  string   `json:"username"`
	Role      string   `json:"role"`
	AvatarURL string   `json:"avatar_url"`
	Aliases   []string `json:"aliases"`
	Emails    []string `json:"emails"`
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !domain.ValidUsername(req.Username) {
		writeError(w, http.StatusBadRequest, fmt.Errorf("username must match ^[a-z0-9_-]{1,32}$"))
		return
	}
	role := domain.RoleUser
	if req.Role != "" {
		if req.Role != string(domain.RoleAdmin) && req.Role != string(domain.RoleUser) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("role must be admin or user"))
			return
		}
		role = domain.Role(req.Role)
	}
	aliases, emails, err := normalizeIdentities(req.Aliases, req.Emails)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	user := domain.User{
		Username: req.Username, Role: role, AvatarURL: req.AvatarURL, Enabled: true,
		Aliases: aliases, Emails: emails,
	}
	created, err := s.store.CreateUser(r.Context(), user)
	if err != nil {
		if db.IsConflict(err) {
			writeError(w, http.StatusConflict, fmt.Errorf("username, alias, or email already in use"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("user not found"))
		return
	}
	writeJSON(w, http.StatusOK, user)
}

type updateUserRequest struct {
	Role      *string   `json:"role"`
	Enabled   *bool     `json:"enabled"`
	AvatarURL *string   `json:"avatar_url"`
	Aliases   *[]string `json:"aliases"`
	Emails    *[]string `json:"emails"`
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("user not found"))
		return
	}
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if req.Role != nil {
		if *req.Role != string(domain.RoleAdmin) && *req.Role != string(domain.RoleUser) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("role must be admin or user"))
			return
		}
		user.Role = domain.Role(*req.Role)
	}
	if req.Enabled != nil {
		user.Enabled = *req.Enabled
	}
	if req.AvatarURL != nil {
		user.AvatarURL = *req.AvatarURL
	}
	aliases := user.Aliases
	if req.Aliases != nil {
		aliases = *req.Aliases
	}
	emails := user.Emails
	if req.Emails != nil {
		emails = *req.Emails
	}
	user.Aliases, user.Emails, err = normalizeIdentities(aliases, emails)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.UpdateUser(r.Context(), user); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, fmt.Errorf("user not found"))
			return
		}
		if db.IsConflict(err) {
			writeError(w, http.StatusConflict, fmt.Errorf("alias or email already in use"))
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	updated, err := s.store.GetUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// deleteUser soft-disables the account, keeping job attribution for audit.
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	user, err := s.store.GetUser(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("user not found"))
		return
	}
	user.Enabled = false
	if err := s.store.UpdateUser(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "disabled", "id": user.ID})
}

func normalizeIdentities(aliases, emails []string) ([]string, []string, error) {
	outAliases := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return nil, nil, fmt.Errorf("aliases must not contain empty values")
		}
		outAliases = append(outAliases, alias)
	}
	outEmails := make([]string, 0, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		if email == "" || !strings.Contains(email, "@") {
			return nil, nil, fmt.Errorf("emails must contain valid addresses")
		}
		outEmails = append(outEmails, email)
	}
	return outAliases, outEmails, nil
}
