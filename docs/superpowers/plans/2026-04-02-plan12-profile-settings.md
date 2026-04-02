# Plan 12: 个人资料与设置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 允许用户更新个人资料（display_name、avatar_url）和应用设置（通知开关、主题、语言），通过专用页面呈现，设置持久化到 `user_settings` 表。

**Architecture:** 服务端扩展 `store.UserStore`（UpdateProfile + UpsertSettings/GetSettings），新增 `handler.ProfileHandler`（PUT /api/users/me）和 `handler.SettingsHandler`（GET/PUT /api/settings）；客户端新增 Profile 页和 Settings 页，通过 Angular signals 实时反映变化。

**Tech Stack:** Go (pgx/v5), Angular 17+ (signals, standalone components, reactive forms), SCSS

---

## 目录结构（Plan 12 新增/修改文件）

```
server/
├── internal/
│   ├── store/
│   │   └── user.go                # MODIFY: add UpdateProfile, GetSettings, UpsertSettings
│   │   └── user_test.go           # MODIFY: add tests for new methods
│   └── handler/
│       ├── profile.go             # NEW: ProfileHandler (PUT /api/users/me)
│       ├── profile_test.go        # NEW
│       ├── settings.go            # NEW: SettingsHandler (GET/PUT /api/settings)
│       └── settings_test.go       # NEW
└── cmd/gateway/main.go            # MODIFY: wire new handlers + routes

client/src/app/
├── core/
│   └── auth/
│       └── auth.service.ts        # MODIFY: add updateProfile method + update signal
├── features/
│   ├── profile/
│   │   ├── profile.component.ts   # NEW
│   │   ├── profile.component.html # NEW
│   │   └── profile.component.scss # NEW
│   └── settings/
│       ├── settings.component.ts  # NEW
│       ├── settings.component.html# NEW
│       └── settings.component.scss# NEW
└── app.routes.ts                  # MODIFY: add /profile and /settings routes
```

---

## Task 1: Profile Update Handler + Tests

**Goal:** `PUT /api/users/me` — allows changing `display_name` and `avatar_url`.

### 1.1 Add `UpdateProfile` to `server/internal/store/user.go`

```go
// UpdateProfile updates the display_name and avatar_url for a user.
// Only non-empty fields are changed.
func (s *UserStore) UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`UPDATE users
		 SET display_name = CASE WHEN $2 != '' THEN $2 ELSE display_name END,
		     avatar_url   = CASE WHEN $3 != '' THEN $3 ELSE avatar_url   END,
		     updated_at   = now()
		 WHERE id = $1
		 RETURNING id, username, email, display_name, avatar_url, status, created_at, updated_at`,
		userID, displayName, avatarURL,
	).Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.AvatarURL,
		&u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}
	return u, nil
}
```

### 1.2 Create `server/internal/handler/profile.go`

```go
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"im-server/internal/model"
)

// ---------- store interface ----------

// ProfileStore is the subset of store.UserStore used by ProfileHandler.
type ProfileStore interface {
	GetByID(ctx context.Context, id int64) (*model.User, error)
	UpdateProfile(ctx context.Context, userID int64, displayName, avatarURL string) (*model.User, error)
}

// ---------- handler ----------

// ProfileHandler serves the profile update endpoint.
type ProfileHandler struct {
	users ProfileStore
	log   *slog.Logger
}

func NewProfileHandler(users ProfileStore, log *slog.Logger) *ProfileHandler {
	return &ProfileHandler{users: users, log: log}
}

// ---------- request type ----------

type updateProfileBody struct {
	DisplayName string `json:"display_name"`
	AvatarURL   string `json:"avatar_url"`
}

// ---------- PUT /api/users/me ----------

// UpdateMe handles PUT /api/users/me.
// Body (all fields optional):
//   { "display_name": "...", "avatar_url": "..." }
func (h *ProfileHandler) UpdateMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body updateProfileBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Validate display_name if provided
	if body.DisplayName != "" {
		body.DisplayName = strings.TrimSpace(body.DisplayName)
		if len(body.DisplayName) < 1 || len(body.DisplayName) > 64 {
			writeError(w, http.StatusUnprocessableEntity, "display_name must be 1-64 characters")
			return
		}
	}

	// Validate avatar_url if provided (basic check)
	if body.AvatarURL != "" && !strings.HasPrefix(body.AvatarURL, "http") {
		writeError(w, http.StatusUnprocessableEntity, "avatar_url must be a valid URL")
		return
	}

	updated, err := h.users.UpdateProfile(r.Context(), claims.UserID, body.DisplayName, body.AvatarURL)
	if err != nil {
		h.log.Error("update profile", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, updated)
}
```

### 1.3 Create `server/internal/handler/profile_test.go`

```go
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stub ----------

type stubProfileStore struct {
	user *model.User
}

func (s *stubProfileStore) GetByID(_ context.Context, _ int64) (*model.User, error) {
	return s.user, nil
}

func (s *stubProfileStore) UpdateProfile(_ context.Context, _ int64, displayName, avatarURL string) (*model.User, error) {
	cp := *s.user
	if displayName != "" {
		cp.DisplayName = displayName
	}
	if avatarURL != "" {
		cp.AvatarURL = avatarURL
	}
	cp.UpdatedAt = time.Now()
	return &cp, nil
}

// ---------- tests ----------

func TestUpdateMe_RequiresAuth(t *testing.T) {
	h := handler.NewProfileHandler(&stubProfileStore{}, testLogger())
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestUpdateMe_InvalidDisplayName(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice", DisplayName: "Alice", Status: model.UserStatusActive}
	h := handler.NewProfileHandler(&stubProfileStore{user: user}, testLogger())

	// 65-character display name
	longName := string(make([]byte, 65))
	for i := range longName {
		longName = longName[:i] + "a" + longName[i+1:]
	}
	body, _ := json.Marshal(map[string]string{"display_name": longName})
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}

func TestUpdateMe_Success(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice", DisplayName: "Alice", Status: model.UserStatusActive}
	h := handler.NewProfileHandler(&stubProfileStore{user: user}, testLogger())

	body, _ := json.Marshal(map[string]string{"display_name": "Alice Updated"})
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp model.User
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.DisplayName != "Alice Updated" {
		t.Errorf("want DisplayName 'Alice Updated', got %q", resp.DisplayName)
	}
}

func TestUpdateMe_InvalidAvatarURL(t *testing.T) {
	user := &model.User{ID: 1, Username: "alice", Status: model.UserStatusActive}
	h := handler.NewProfileHandler(&stubProfileStore{user: user}, testLogger())
	body, _ := json.Marshal(map[string]string{"avatar_url": "not-a-url"})
	req := httptest.NewRequest(http.MethodPut, "/api/users/me", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateMe(w, req)
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d", w.Code)
	}
}
```

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/server
go build ./internal/...
go test ./internal/handler/... -run TestUpdateMe -v
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/user.go server/internal/handler/profile.go server/internal/handler/profile_test.go
git commit -m "feat: add UpdateProfile store method + ProfileHandler PUT /api/users/me"
```

---

## Task 2: User Settings Handler + Tests

**Goal:** `GET /api/settings` and `PUT /api/settings` — persist per-user notification, theme, and language preferences.

### 2.1 Add settings methods to `server/internal/store/user.go`

```go
// GetSettings returns the settings row for a user. If none exists, returns
// a default settings object (not persisted until UpsertSettings is called).
func (s *UserStore) GetSettings(ctx context.Context, userID int64) (*model.UserSettings, error) {
	settings := &model.UserSettings{
		UserID:              userID,
		NotificationEnabled: true,
		Theme:               "system",
		Language:            "en",
	}
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, notification_enabled, theme, language, settings_json
		 FROM user_settings WHERE user_id = $1`, userID,
	).Scan(&settings.UserID, &settings.NotificationEnabled, &settings.Theme,
		&settings.Language, &settings.SettingsJSON)
	if err != nil {
		// Not found → return defaults without error
		if isNotFound(err) {
			return settings, nil
		}
		return nil, fmt.Errorf("get settings: %w", err)
	}
	return settings, nil
}

// UpsertSettings creates or updates the user_settings row.
func (s *UserStore) UpsertSettings(ctx context.Context, settings *model.UserSettings) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO user_settings (user_id, notification_enabled, theme, language, settings_json)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id) DO UPDATE
		   SET notification_enabled = EXCLUDED.notification_enabled,
		       theme                = EXCLUDED.theme,
		       language             = EXCLUDED.language,
		       settings_json        = EXCLUDED.settings_json`,
		settings.UserID, settings.NotificationEnabled, settings.Theme,
		settings.Language, settings.SettingsJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert settings: %w", err)
	}
	return nil
}

// isNotFound returns true when pgx returns pgx.ErrNoRows.
func isNotFound(err error) bool {
	return err != nil && err.Error() == "no rows in result set"
}
```

### 2.2 Create `server/internal/handler/settings.go`

```go
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"im-server/internal/model"
)

// ---------- store interface ----------

// SettingsStore is the subset of store.UserStore used by SettingsHandler.
type SettingsStore interface {
	GetSettings(ctx context.Context, userID int64) (*model.UserSettings, error)
	UpsertSettings(ctx context.Context, settings *model.UserSettings) error
}

// ---------- handler ----------

// SettingsHandler serves the user settings endpoints.
type SettingsHandler struct {
	store SettingsStore
	log   *slog.Logger
}

func NewSettingsHandler(store SettingsStore, log *slog.Logger) *SettingsHandler {
	return &SettingsHandler{store: store, log: log}
}

// ---------- request type ----------

type updateSettingsBody struct {
	NotificationEnabled *bool  `json:"notification_enabled"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
}

// ---------- GET /api/settings ----------

// GetSettings handles GET /api/settings.
func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	settings, err := h.store.GetSettings(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("get settings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, settings)
}

// ---------- PUT /api/settings ----------

// UpdateSettings handles PUT /api/settings.
// Partial updates are supported: only provided fields override the current values.
func (h *SettingsHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	claims, ok := claimsFromCtx(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body updateSettingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Load existing settings first to support partial updates
	existing, err := h.store.GetSettings(r.Context(), claims.UserID)
	if err != nil {
		h.log.Error("get settings for update", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Apply partial update
	if body.NotificationEnabled != nil {
		existing.NotificationEnabled = *body.NotificationEnabled
	}
	if body.Theme != "" {
		existing.Theme = body.Theme
	}
	if body.Language != "" {
		existing.Language = body.Language
	}

	if err := h.store.UpsertSettings(r.Context(), existing); err != nil {
		h.log.Error("upsert settings", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}
```

### 2.3 Create `server/internal/handler/settings_test.go`

```go
package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stub ----------

type stubSettingsStore struct {
	settings *model.UserSettings
}

func newStubSettingsStore() *stubSettingsStore {
	return &stubSettingsStore{
		settings: &model.UserSettings{
			UserID:              1,
			NotificationEnabled: true,
			Theme:               "system",
			Language:            "en",
		},
	}
}

func (s *stubSettingsStore) GetSettings(_ context.Context, _ int64) (*model.UserSettings, error) {
	return s.settings, nil
}

func (s *stubSettingsStore) UpsertSettings(_ context.Context, settings *model.UserSettings) error {
	*s.settings = *settings
	return nil
}

// ---------- tests ----------

func TestGetSettings_RequiresAuth(t *testing.T) {
	h := handler.NewSettingsHandler(newStubSettingsStore(), testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	w := httptest.NewRecorder()
	h.GetSettings(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestGetSettings_ReturnsDefaults(t *testing.T) {
	h := handler.NewSettingsHandler(newStubSettingsStore(), testLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.GetSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var s model.UserSettings
	if err := json.NewDecoder(w.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Theme != "system" {
		t.Errorf("want Theme 'system', got %q", s.Theme)
	}
}

func TestUpdateSettings_PartialUpdate(t *testing.T) {
	store := newStubSettingsStore()
	h := handler.NewSettingsHandler(store, testLogger())

	body, _ := json.Marshal(map[string]string{"theme": "dark"})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	var s model.UserSettings
	_ = json.NewDecoder(w.Body).Decode(&s)
	if s.Theme != "dark" {
		t.Errorf("want Theme 'dark', got %q", s.Theme)
	}
	// Language should still be the default
	if s.Language != "en" {
		t.Errorf("want Language 'en', got %q", s.Language)
	}
}

func TestUpdateSettings_NotificationToggle(t *testing.T) {
	store := newStubSettingsStore()
	h := handler.NewSettingsHandler(store, testLogger())

	notifFalse := false
	body, _ := json.Marshal(map[string]any{"notification_enabled": notifFalse})
	req := httptest.NewRequest(http.MethodPut, "/api/settings", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req = withClaims(req, 1, "alice")
	w := httptest.NewRecorder()
	h.UpdateSettings(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var s model.UserSettings
	_ = json.NewDecoder(w.Body).Decode(&s)
	if s.NotificationEnabled {
		t.Error("expected notification_enabled to be false")
	}
}
```

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/server
go build ./internal/...
go test ./internal/handler/... -run "TestGetSettings|TestUpdateSettings" -v
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/user.go server/internal/handler/settings.go server/internal/handler/settings_test.go
git commit -m "feat: add GetSettings/UpsertSettings store methods + SettingsHandler"
```

---

## Task 3: Wire Routes in Gateway

### 3.1 Modify `server/cmd/gateway/main.go`

After `authHandler` creation, add:

```go
profileHandler := handler.NewProfileHandler(userStore, log)
settingsHandler := handler.NewSettingsHandler(userStore, log)
```

Add routes after the existing `GET /api/auth/me` route:

```go
// Profile route (JWT protected)
mux.Handle("PUT /api/users/me", jwtMiddleware(http.HandlerFunc(profileHandler.UpdateMe)))

// Settings routes (JWT protected)
mux.Handle("GET /api/settings", jwtMiddleware(http.HandlerFunc(settingsHandler.GetSettings)))
mux.Handle("PUT /api/settings", jwtMiddleware(http.HandlerFunc(settingsHandler.UpdateSettings)))
```

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/server
go build ./cmd/gateway/
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): wire PUT /api/users/me and GET|PUT /api/settings routes"
```

---

## Task 4: Client Profile Page

**Goal:** A settings-style page at `/profile` to edit display name and avatar URL.

### 4.1 Modify `client/src/app/core/auth/auth.service.ts`

Add `updateProfile` method to `AuthService`:

```typescript
/** Update the current user's display_name and/or avatar_url. */
async updateProfile(displayName: string, avatarURL: string): Promise<void> {
  const updated = await firstValueFrom(
    this.http.put<User>(`${API_BASE}/users/me`, { display_name: displayName, avatar_url: avatarURL }),
  );
  this.currentUser.set(updated);
}
```

### 4.2 Create `client/src/app/features/profile/profile.component.ts`

```typescript
import { Component, OnInit, inject, signal, computed } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { AuthService } from '../../core/auth/auth.service';

@Component({
  selector: 'app-profile',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './profile.component.html',
  styleUrls: ['./profile.component.scss'],
})
export class ProfileComponent implements OnInit {
  private authService = inject(AuthService);

  user = this.authService.currentUser;

  displayName = signal('');
  avatarURL = signal('');
  saving = signal(false);
  success = signal(false);
  error = signal<string | null>(null);

  ngOnInit(): void {
    const u = this.user();
    if (u) {
      this.displayName.set(u.display_name);
      this.avatarURL.set(u.avatar_url ?? '');
    }
  }

  async save(): Promise<void> {
    this.saving.set(true);
    this.success.set(false);
    this.error.set(null);
    try {
      await this.authService.updateProfile(this.displayName(), this.avatarURL());
      this.success.set(true);
    } catch {
      this.error.set('Failed to update profile. Please try again.');
    } finally {
      this.saving.set(false);
    }
  }

  get hasChanges(): boolean {
    const u = this.user();
    if (!u) return false;
    return this.displayName() !== u.display_name || this.avatarURL() !== (u.avatar_url ?? '');
  }
}
```

### 4.3 Create `client/src/app/features/profile/profile.component.html`

```html
<div class="profile-page">
  <div class="page-header">
    <h2>Edit Profile</h2>
  </div>

  @if (user()) {
    <div class="profile-card">
      <!-- Avatar preview -->
      <div class="avatar-section">
        <div class="avatar-preview">
          @if (avatarURL()) {
            <img [src]="avatarURL()" alt="Avatar preview" />
          } @else {
            <div class="avatar-placeholder">
              {{ (user()?.display_name ?? 'U')[0].toUpperCase() }}
            </div>
          }
        </div>
        <div class="avatar-hint">Enter a URL below to update your avatar</div>
      </div>

      <form (ngSubmit)="save()" #f="ngForm">
        <!-- Username (read-only) -->
        <div class="form-group">
          <label>Username</label>
          <input
            type="text"
            class="form-control readonly"
            [value]="user()?.username"
            readonly
          />
          <span class="hint">Username cannot be changed.</span>
        </div>

        <!-- Display name -->
        <div class="form-group">
          <label for="displayName">Display Name</label>
          <input
            id="displayName"
            type="text"
            class="form-control"
            placeholder="Your display name"
            maxlength="64"
            [ngModel]="displayName()"
            (ngModelChange)="displayName.set($event)"
            name="displayName"
            required
          />
        </div>

        <!-- Avatar URL -->
        <div class="form-group">
          <label for="avatarURL">Avatar URL</label>
          <input
            id="avatarURL"
            type="url"
            class="form-control"
            placeholder="https://example.com/avatar.png"
            [ngModel]="avatarURL()"
            (ngModelChange)="avatarURL.set($event)"
            name="avatarURL"
          />
        </div>

        @if (success()) {
          <div class="alert alert-success">Profile updated successfully!</div>
        }

        @if (error()) {
          <div class="alert alert-error">{{ error() }}</div>
        }

        <button
          type="submit"
          class="btn-primary"
          [disabled]="saving() || !hasChanges"
        >
          {{ saving() ? 'Saving...' : 'Save Changes' }}
        </button>
      </form>
    </div>
  }
</div>
```

### 4.4 Create `client/src/app/features/profile/profile.component.scss`

```scss
.profile-page {
  max-width: 560px;
  margin: 0 auto;
  padding: 32px 24px;
}

.page-header {
  margin-bottom: 24px;

  h2 {
    font-size: 22px;
    font-weight: 700;
    color: var(--text-primary, #111);
    margin: 0;
  }
}

.profile-card {
  background: var(--card-bg, #fff);
  border: 1px solid var(--border-color, #e0e0e0);
  border-radius: 12px;
  padding: 28px;
}

.avatar-section {
  display: flex;
  align-items: center;
  gap: 20px;
  margin-bottom: 28px;

  .avatar-preview {
    width: 72px;
    height: 72px;
    border-radius: 50%;
    overflow: hidden;
    flex-shrink: 0;

    img {
      width: 100%;
      height: 100%;
      object-fit: cover;
    }

    .avatar-placeholder {
      width: 100%;
      height: 100%;
      background: var(--accent, #5865f2);
      color: #fff;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 28px;
      font-weight: 700;
    }
  }

  .avatar-hint {
    font-size: 13px;
    color: var(--text-secondary, #888);
  }
}

.form-group {
  margin-bottom: 20px;

  label {
    display: block;
    font-size: 13px;
    font-weight: 600;
    color: var(--text-secondary, #555);
    margin-bottom: 6px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }

  .form-control {
    width: 100%;
    padding: 10px 14px;
    font-size: 14px;
    border: 1px solid var(--border-color, #ccc);
    border-radius: 8px;
    background: var(--input-bg, #fff);
    color: var(--text-primary, #111);
    box-sizing: border-box;
    outline: none;
    transition: border-color 0.15s, box-shadow 0.15s;

    &:focus {
      border-color: var(--accent, #5865f2);
      box-shadow: 0 0 0 2px rgba(88, 101, 242, 0.2);
    }

    &.readonly {
      background: var(--disabled-bg, #f5f5f5);
      color: var(--text-secondary, #888);
      cursor: not-allowed;
    }
  }

  .hint {
    display: block;
    margin-top: 4px;
    font-size: 12px;
    color: var(--text-secondary, #888);
  }
}

.alert {
  padding: 10px 14px;
  border-radius: 8px;
  font-size: 14px;
  margin-bottom: 16px;

  &.alert-success {
    background: #d4edda;
    color: #155724;
    border: 1px solid #c3e6cb;
  }

  &.alert-error {
    background: #f8d7da;
    color: #721c24;
    border: 1px solid #f5c6cb;
  }
}

.btn-primary {
  width: 100%;
  padding: 10px 20px;
  background: var(--accent, #5865f2);
  color: #fff;
  border: none;
  border-radius: 8px;
  font-size: 15px;
  font-weight: 600;
  cursor: pointer;
  transition: opacity 0.15s;

  &:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  &:hover:not(:disabled) {
    opacity: 0.9;
  }
}
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/auth/auth.service.ts client/src/app/features/profile/
git commit -m "feat(client): add profile page with display name and avatar editing"
```

---

## Task 5: Client Settings Page

**Goal:** `/settings` — notification toggle, theme selector (light/dark/system), language selector.

### 5.1 Create `client/src/app/features/settings/settings.component.ts`

```typescript
import { Component, OnInit, inject, signal } from '@angular/core';
import { CommonModule } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { HttpClient } from '@angular/common/http';
import { firstValueFrom } from 'rxjs';

const API_BASE = 'http://localhost:8080/api';

export interface UserSettings {
  user_id: number;
  notification_enabled: boolean;
  theme: string;
  language: string;
}

@Component({
  selector: 'app-settings',
  standalone: true,
  imports: [CommonModule, FormsModule],
  templateUrl: './settings.component.html',
  styleUrls: ['./settings.component.scss'],
})
export class SettingsComponent implements OnInit {
  private http = inject(HttpClient);

  settings = signal<UserSettings | null>(null);
  loading = signal(false);
  saving = signal(false);
  success = signal(false);
  error = signal<string | null>(null);

  readonly themes = [
    { value: 'system', label: 'System Default' },
    { value: 'light', label: 'Light' },
    { value: 'dark', label: 'Dark' },
  ];

  readonly languages = [
    { value: 'en', label: 'English' },
    { value: 'zh', label: '中文' },
    { value: 'ja', label: '日本語' },
    { value: 'ko', label: '한국어' },
  ];

  async ngOnInit(): Promise<void> {
    this.loading.set(true);
    try {
      const s = await firstValueFrom(
        this.http.get<UserSettings>(`${API_BASE}/settings`),
      );
      this.settings.set(s);
    } catch {
      this.error.set('Failed to load settings.');
    } finally {
      this.loading.set(false);
    }
  }

  updateNotification(value: boolean): void {
    this.settings.update(s => s ? { ...s, notification_enabled: value } : s);
  }

  updateTheme(value: string): void {
    this.settings.update(s => s ? { ...s, theme: value } : s);
  }

  updateLanguage(value: string): void {
    this.settings.update(s => s ? { ...s, language: value } : s);
  }

  async save(): Promise<void> {
    const s = this.settings();
    if (!s) return;
    this.saving.set(true);
    this.success.set(false);
    this.error.set(null);
    try {
      const updated = await firstValueFrom(
        this.http.put<UserSettings>(`${API_BASE}/settings`, {
          notification_enabled: s.notification_enabled,
          theme: s.theme,
          language: s.language,
        }),
      );
      this.settings.set(updated);
      this.success.set(true);
    } catch {
      this.error.set('Failed to save settings.');
    } finally {
      this.saving.set(false);
    }
  }
}
```

### 5.2 Create `client/src/app/features/settings/settings.component.html`

```html
<div class="settings-page">
  <div class="page-header">
    <h2>Settings</h2>
  </div>

  @if (loading()) {
    <div class="loading">Loading settings...</div>
  }

  @if (error() && !loading()) {
    <div class="alert alert-error">{{ error() }}</div>
  }

  @if (settings() && !loading()) {
    <div class="settings-sections">

      <!-- Notifications section -->
      <div class="settings-section">
        <h3 class="section-title">Notifications</h3>
        <div class="setting-row">
          <div class="setting-info">
            <div class="setting-label">Enable Notifications</div>
            <div class="setting-desc">Receive desktop notifications for new messages.</div>
          </div>
          <label class="toggle">
            <input
              type="checkbox"
              [checked]="settings()!.notification_enabled"
              (change)="updateNotification($any($event.target).checked)"
            />
            <span class="toggle-slider"></span>
          </label>
        </div>
      </div>

      <!-- Appearance section -->
      <div class="settings-section">
        <h3 class="section-title">Appearance</h3>
        <div class="setting-row">
          <div class="setting-info">
            <div class="setting-label">Theme</div>
            <div class="setting-desc">Choose your preferred color theme.</div>
          </div>
          <select
            class="setting-select"
            [value]="settings()!.theme"
            (change)="updateTheme($any($event.target).value)"
          >
            @for (t of themes; track t.value) {
              <option [value]="t.value">{{ t.label }}</option>
            }
          </select>
        </div>
      </div>

      <!-- Language section -->
      <div class="settings-section">
        <h3 class="section-title">Language</h3>
        <div class="setting-row">
          <div class="setting-info">
            <div class="setting-label">Display Language</div>
            <div class="setting-desc">Set the language for the application interface.</div>
          </div>
          <select
            class="setting-select"
            [value]="settings()!.language"
            (change)="updateLanguage($any($event.target).value)"
          >
            @for (l of languages; track l.value) {
              <option [value]="l.value">{{ l.label }}</option>
            }
          </select>
        </div>
      </div>

    </div>

    @if (success()) {
      <div class="alert alert-success">Settings saved!</div>
    }

    <div class="settings-actions">
      <button
        class="btn-primary"
        [disabled]="saving()"
        (click)="save()"
      >
        {{ saving() ? 'Saving...' : 'Save Settings' }}
      </button>
    </div>
  }
</div>
```

### 5.3 Create `client/src/app/features/settings/settings.component.scss`

```scss
.settings-page {
  max-width: 640px;
  margin: 0 auto;
  padding: 32px 24px;
}

.page-header {
  margin-bottom: 24px;

  h2 {
    font-size: 22px;
    font-weight: 700;
    color: var(--text-primary, #111);
    margin: 0;
  }
}

.loading {
  text-align: center;
  padding: 48px;
  color: var(--text-secondary, #888);
}

.settings-sections {
  display: flex;
  flex-direction: column;
  gap: 24px;
}

.settings-section {
  background: var(--card-bg, #fff);
  border: 1px solid var(--border-color, #e0e0e0);
  border-radius: 12px;
  padding: 20px 24px;

  .section-title {
    font-size: 14px;
    font-weight: 700;
    color: var(--text-secondary, #555);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    margin: 0 0 16px 0;
  }
}

.setting-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 8px 0;

  & + .setting-row {
    border-top: 1px solid var(--border-color, #eee);
    padding-top: 16px;
    margin-top: 8px;
  }

  .setting-info {
    flex: 1;
    min-width: 0;

    .setting-label {
      font-size: 14px;
      font-weight: 600;
      color: var(--text-primary, #111);
      margin-bottom: 2px;
    }

    .setting-desc {
      font-size: 12px;
      color: var(--text-secondary, #888);
    }
  }
}

// Toggle switch
.toggle {
  position: relative;
  display: inline-block;
  width: 44px;
  height: 24px;
  flex-shrink: 0;

  input {
    opacity: 0;
    width: 0;
    height: 0;
  }

  .toggle-slider {
    position: absolute;
    inset: 0;
    background: var(--disabled-bg, #ccc);
    border-radius: 24px;
    cursor: pointer;
    transition: background 0.2s;

    &::before {
      content: '';
      position: absolute;
      width: 18px;
      height: 18px;
      left: 3px;
      top: 3px;
      background: #fff;
      border-radius: 50%;
      transition: transform 0.2s;
    }
  }

  input:checked + .toggle-slider {
    background: var(--accent, #5865f2);

    &::before {
      transform: translateX(20px);
    }
  }
}

.setting-select {
  padding: 8px 12px;
  border: 1px solid var(--border-color, #ccc);
  border-radius: 8px;
  background: var(--input-bg, #fff);
  color: var(--text-primary, #111);
  font-size: 14px;
  cursor: pointer;
  outline: none;
  min-width: 140px;

  &:focus {
    border-color: var(--accent, #5865f2);
    box-shadow: 0 0 0 2px rgba(88, 101, 242, 0.2);
  }
}

.settings-actions {
  margin-top: 24px;

  .btn-primary {
    padding: 10px 28px;
    background: var(--accent, #5865f2);
    color: #fff;
    border: none;
    border-radius: 8px;
    font-size: 15px;
    font-weight: 600;
    cursor: pointer;
    transition: opacity 0.15s;

    &:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    &:hover:not(:disabled) {
      opacity: 0.9;
    }
  }
}

.alert {
  padding: 10px 14px;
  border-radius: 8px;
  font-size: 14px;
  margin: 16px 0 0;

  &.alert-success {
    background: #d4edda;
    color: #155724;
    border: 1px solid #c3e6cb;
  }

  &.alert-error {
    background: #f8d7da;
    color: #721c24;
    border: 1px solid #f5c6cb;
  }
}
```

### 5.4 Register routes in `client/src/app/app.routes.ts`

```typescript
{
  path: 'profile',
  loadComponent: () =>
    import('./features/profile/profile.component').then(m => m.ProfileComponent),
},
{
  path: 'settings',
  loadComponent: () =>
    import('./features/settings/settings.component').then(m => m.SettingsComponent),
},
```

Also add nav links to these pages in the sidebar (`main-layout.component.html`), e.g., a gear icon linking to `/settings` and a user icon linking to `/profile`.

**Commands:**
```bash
cd /Users/mac17/workspace/ai/im/client
npm run build -- --configuration=development
```

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/features/settings/ client/src/app/app.routes.ts
git commit -m "feat(client): add settings page (notification toggle, theme, language)"
```

---

## Task 6: Integration Verification

- [ ] Server builds: `cd server && go build ./...`
- [ ] Handler tests pass: `go test ./internal/handler/... -run "TestUpdateMe|TestGetSettings|TestUpdateSettings" -v`
- [ ] Client builds: `cd client && npm run build`
- [ ] Navigate to `/profile` — current display name and avatar URL pre-populated
- [ ] Change display name → save → name updates in channel list and chat header (via `currentUser` signal)
- [ ] Enter invalid avatar URL → save blocked with 422 error message
- [ ] Navigate to `/settings` — current settings loaded from server
- [ ] Toggle notification off → save → reload page → notification_enabled is false
- [ ] Change theme to "dark" → save → confirm persisted (check PG `user_settings` table)
- [ ] Unauthenticated `PUT /api/users/me` returns 401
- [ ] Unauthenticated `GET /api/settings` returns 401
- [ ] `PUT /api/settings` with partial body only updates provided fields

**Commit:**
```bash
cd /Users/mac17/workspace/ai/im
git add .
git commit -m "feat: Plan 12 profile & settings — server + client integration complete"
```
