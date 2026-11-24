package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"videograber/internal/auth"
	"videograber/internal/jobs"
	"videograber/internal/store"
)

var reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// libraryUser returns the user for library endpoints (individual mode only).
func (s *Server) libraryUser(w http.ResponseWriter, r *http.Request) *store.User {
	u := userFrom(r)
	if u == nil {
		writeError(w, http.StatusNotFound, "history and categories are available in individual mode")
	}
	return u
}

func (s *Server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "already exists")
	default:
		s.internalError(w, r, err)
	}
}

// ---------- categories ----------

type categoryRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (c *categoryRequest) validate() error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" || len([]rune(c.Name)) > 60 {
		return errors.New("category name must be 1-60 characters long")
	}
	if c.Color != "" && !reColor.MatchString(c.Color) {
		return errors.New("color must look like #a1b2c3")
	}
	return nil
}

func (s *Server) handleListCategories(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	cats, err := s.store.ListCategories(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cats)
}

func (s *Server) handleCreateCategory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	var req categoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.store.CreateCategory(r.Context(), u.ID, req.Name, req.Color)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateCategory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req categoryRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := req.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.store.UpdateCategory(r.Context(), u.ID, id, req.Name, req.Color); err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteCategory(r.Context(), u.ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------- history ----------

func (s *Server) handleListHistory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	q := r.URL.Query()
	hq := store.HistoryQuery{Search: strings.TrimSpace(q.Get("q"))}
	hq.CategoryID, _ = strconv.ParseInt(q.Get("category"), 10, 64)
	hq.Limit, _ = strconv.Atoi(q.Get("limit"))
	hq.Offset, _ = strconv.Atoi(q.Get("offset"))
	items, total, err := s.store.ListHistory(r.Context(), u.ID, hq)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (s *Server) handleUpdateHistory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		CategoryID *int64 `json:"category_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.CategoryID != nil && !s.store.CategoryOwned(r.Context(), u.ID, *req.CategoryID) {
		writeError(w, http.StatusBadRequest, "unknown category")
		return
	}
	if err := s.store.SetHistoryCategory(r.Context(), u.ID, id, req.CategoryID); err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteHistory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteHistory(r.Context(), u.ID, id); err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleClearHistory(w http.ResponseWriter, r *http.Request) {
	u := s.libraryUser(w, r)
	if u == nil {
		return
	}
	if err := s.store.ClearHistory(r.Context(), u.ID); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// OnJobFinished records the final state of a job in the history.
func (s *Server) OnJobFinished(j jobs.Job) {
	if j.HistoryID == 0 {
		return
	}
	status := string(j.State)
	if err := s.store.FinishHistory(contextBG(), j.HistoryID, status, j.Error, j.Size); err != nil {
		s.log.Warn("cannot update history", "err", err)
	}
}

// ---------- administration ----------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateCredentials(credentials{Username: req.Username, Password: req.Password}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.store.CreateUser(r.Context(), req.Username, hash, req.IsAdmin)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	s.log.Info("user created", "user", u.Username, "by", userFrom(r).Username)
	writeJSON(w, http.StatusCreated, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		IsAdmin  *bool   `json:"is_admin"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	target, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	me := userFrom(r)
	admin, disabled := target.IsAdmin, target.Disabled
	if req.IsAdmin != nil {
		admin = *req.IsAdmin
	}
	if req.Disabled != nil {
		disabled = *req.Disabled
	}
	if target.ID == me.ID && (!admin || disabled) {
		writeError(w, http.StatusBadRequest, "you cannot demote or disable yourself")
		return
	}
	if err := s.store.UpdateUser(r.Context(), id, admin, disabled); err != nil {
		s.storeError(w, r, err)
		return
	}
	if disabled {
		_ = s.store.DeleteUserSessions(r.Context(), id, "")
	}
	if req.Password != nil {
		if err := auth.ValidatePassword(*req.Password); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		hash, err := auth.HashPassword(*req.Password)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if err := s.store.SetPassword(r.Context(), id, hash); err != nil {
			s.storeError(w, r, err)
			return
		}
		_ = s.store.DeleteUserSessions(r.Context(), id, "")
	}
	u, _ := s.store.UserByID(r.Context(), id)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if id == userFrom(r).ID {
		writeError(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleListInvites(w http.ResponseWriter, r *http.Request) {
	inv, err := s.store.ListInvites(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	type view struct {
		*store.Invite
		Usable bool `json:"usable"`
	}
	out := []view{}
	for _, i := range inv {
		out = append(out, view{Invite: i, Usable: i.Usable()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Note     string `json:"note"`
		MaxUses  int    `json:"max_uses"`
		TTLHours int    `json:"ttl_hours"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(req.Note) > 200 {
		writeError(w, http.StatusBadRequest, "note is too long")
		return
	}
	inv, err := s.store.CreateInvite(r.Context(), auth.RandomToken(18), strings.TrimSpace(req.Note), userFrom(r).ID,
		time.Duration(req.TTLHours)*time.Hour, req.MaxUses)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleDeleteInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteInvite(r.Context(), id); err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func contextBG() context.Context { return context.Background() }
