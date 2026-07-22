//go:build !lite

package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/admintoken"
	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/store"
)

const maxAdminTokenLifetime = 10 * 365 * 24 * time.Hour

type adminAPITokenView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"token_prefix"`
	Scopes     []string `json:"scopes"`
	Enabled    bool     `json:"enabled"`
	Note       string   `json:"note,omitempty"`
	CreatedBy  string   `json:"created_by,omitempty"`
	CreatedAt  int64    `json:"created_at"`
	ExpiresAt  int64    `json:"expires_at,omitempty"`
	LastUsedAt int64    `json:"last_used_at,omitempty"`
	RevokedAt  int64    `json:"revoked_at,omitempty"`
	Revision   int64    `json:"revision"`
	UpdatedAt  int64    `json:"updated_at"`
}

func publicAdminAPIToken(row store.AdminAPITokenRow) adminAPITokenView {
	return adminAPITokenView{
		ID: row.ID, Name: row.Name, Prefix: row.Prefix,
		Scopes: admintoken.DecodeScopes(row.ScopeJSON), Enabled: row.Enabled, Note: row.Note,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt,
		LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt, Revision: row.Revision,
		UpdatedAt: row.UpdatedAt,
	}
}

func (s *Server) requireSessionAdmin(w http.ResponseWriter, r *http.Request) bool {
	principal := principalFrom(r)
	if principal.Kind != "session" || principal.Role != "admin" {
		writeAppErr(w, apperr.New(apperr.CodeForbidden, http.StatusForbidden, "administrator session required"))
		return false
	}
	return true
}

func (s *Server) handleAdminListAPITokens(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	rows, err := s.deps.Store.ListAdminAPITokens()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	views := make([]adminAPITokenView, 0, len(rows))
	for _, row := range rows {
		views = append(views, publicAdminAPIToken(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": views, "available_scopes": admintoken.AllScopes})
}

func (s *Server) handleAdminCreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	var request struct {
		Name         string   `json:"name"`
		Note         string   `json:"note"`
		Scopes       []string `json:"scopes"`
		ExpiresInSec int64    `json:"expires_in_sec"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "invalid API token request"))
		return
	}
	if strings.TrimSpace(request.Name) == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "API token name required"))
		return
	}
	if request.ExpiresInSec < 0 || request.ExpiresInSec > int64(maxAdminTokenLifetime/time.Second) {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "API token lifetime invalid"))
		return
	}
	expiresAt := int64(0)
	if request.ExpiresInSec > 0 {
		expiresAt = time.Now().Add(time.Duration(request.ExpiresInSec) * time.Second).Unix()
	}
	plain, row, err := admintoken.NewRow(request.Name, request.Note, principalFrom(r).Subject, request.Scopes, expiresAt)
	if err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusUnprocessableEntity, err.Error()))
		return
	}
	if err := s.deps.Store.InsertAdminAPIToken(row); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": plain, "credential": publicAdminAPIToken(row),
		"warning": "store this token now; it will not be shown again",
	})
}

func (s *Server) handleAdminUpdateAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	row, ok := s.adminAPITokenByID(w, r.PathValue("id"))
	if !ok {
		return
	}
	var request struct {
		Name      *string  `json:"name"`
		Note      *string  `json:"note"`
		Scopes    []string `json:"scopes"`
		Enabled   *bool    `json:"enabled"`
		ExpiresAt *int64   `json:"expires_at"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "invalid API token update"))
		return
	}
	if request.Name != nil {
		row.Name = strings.TrimSpace(*request.Name)
	}
	if row.Name == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "API token name required"))
		return
	}
	if request.Note != nil {
		row.Note = strings.TrimSpace(*request.Note)
	}
	if request.Scopes != nil {
		encoded, err := admintoken.EncodeScopes(request.Scopes)
		if err != nil {
			writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusUnprocessableEntity, err.Error()))
			return
		}
		row.ScopeJSON = encoded
	}
	if request.Enabled != nil {
		row.Enabled = *request.Enabled
	}
	if request.ExpiresAt != nil {
		if *request.ExpiresAt < 0 || *request.ExpiresAt > time.Now().Add(maxAdminTokenLifetime).Unix() {
			writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "API token expiry invalid"))
			return
		}
		row.ExpiresAt = *request.ExpiresAt
	}
	if err := s.deps.Store.UpdateAdminAPIToken(row, expectedRevision(r)); err != nil {
		writeAdminAPITokenMutationError(w, err)
		return
	}
	updated, _, err := s.deps.Store.GetAdminAPITokenByID(row.ID)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"credential": publicAdminAPIToken(updated)})
}

func (s *Server) handleAdminRotateAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	row, ok := s.adminAPITokenByID(w, r.PathValue("id"))
	if !ok {
		return
	}
	plain, err := admintoken.Generate()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if err := s.deps.Store.RotateAdminAPIToken(row.ID, admintoken.Hash(plain), admintoken.DisplayPrefix(plain), expectedRevision(r)); err != nil {
		writeAdminAPITokenMutationError(w, err)
		return
	}
	updated, _, err := s.deps.Store.GetAdminAPITokenByID(row.ID)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token": plain, "credential": publicAdminAPIToken(updated),
		"warning": "store this token now; it will not be shown again",
	})
}

func (s *Server) handleAdminRevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	if err := s.deps.Store.RevokeAdminAPIToken(r.PathValue("id"), expectedRevision(r)); err != nil {
		writeAdminAPITokenMutationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminDeleteAPIToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	if err := s.deps.Store.DeleteAdminAPIToken(r.PathValue("id"), expectedRevision(r)); err != nil {
		writeAdminAPITokenMutationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminAPITokenLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	logs, err := s.deps.Store.ListAdminAPITokenLogs(100)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
}

func (s *Server) adminAPITokenByID(w http.ResponseWriter, id string) (store.AdminAPITokenRow, bool) {
	row, found, err := s.deps.Store.GetAdminAPITokenByID(id)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return store.AdminAPITokenRow{}, false
	}
	if !found {
		writeAppErr(w, apperr.ErrNotFound)
		return store.AdminAPITokenRow{}, false
	}
	return row, true
}

func writeAdminAPITokenMutationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "API token was updated elsewhere or If-Match is missing"))
		return
	}
	writeAppErr(w, apperr.Internal(err))
}
