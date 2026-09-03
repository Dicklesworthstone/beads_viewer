package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/beads_viewer/internal/env"
	"github.com/Dicklesworthstone/beads_viewer/pkg/debug"
)

// savedConfigDisabled reports whether BV_NO_SAVED_CONFIG is set, in which case
// bv neither reads nor writes anything under the user config directory.
func savedConfigDisabled() bool {
	return env.NoSavedConfig.Get() != ""
}

// TutorialProgress tracks which tutorial pages have been viewed.
// This persists across sessions so users can see their progress.
type TutorialProgress struct {
	ViewedPages    map[string]bool `json:"viewed_pages"`     // page ID → viewed
	LastPageID     string          `json:"last_page_id"`     // Resume point
	LastViewedTime time.Time       `json:"last_viewed_time"` // When last viewed
	CompletedOnce  bool            `json:"completed_once"`   // Has seen all pages at least once
}

// tutorialProgressManager handles saving/loading of tutorial progress.
// Uses a mutex for thread-safe operations.
type tutorialProgressManager struct {
	mu       sync.Mutex
	progress *TutorialProgress
	dirty    bool // Has unsaved changes
}

var (
	progressManager     *tutorialProgressManager
	progressManagerOnce sync.Once
)

// GetTutorialProgressManager returns the singleton progress manager.
func GetTutorialProgressManager() *tutorialProgressManager {
	progressManagerOnce.Do(func() {
		progressManager = &tutorialProgressManager{
			progress: &TutorialProgress{
				ViewedPages: make(map[string]bool),
			},
		}
		// Load existing progress on first access
		progressManager.Load()
	})
	return progressManager
}

// TutorialProgressPath returns the path to the tutorial progress config file:
// $XDG_CONFIG_HOME/bv/tutorial-progress.json when XDG_CONFIG_HOME is set,
// otherwise ~/.config/bv/tutorial-progress.json.
func TutorialProgressPath() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "bv", "tutorial-progress.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "bv", "tutorial-progress.json")
}

// Load reads tutorial progress from disk. With BV_NO_SAVED_CONFIG set it
// leaves the in-memory state untouched and reads nothing.
func (m *tutorialProgressManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if savedConfigDisabled() {
		return nil
	}

	path := TutorialProgressPath()
	if path == "" {
		return nil // Can't determine path, start fresh
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No saved progress, start fresh
			m.progress = &TutorialProgress{
				ViewedPages: make(map[string]bool),
			}
			return nil
		}
		return err
	}

	var progress TutorialProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		// Corrupt file: ignore it and start fresh; the next Save overwrites it.
		debug.Log("tutorial progress: ignoring corrupt %s: %v", path, err)
		m.progress = &TutorialProgress{
			ViewedPages: make(map[string]bool),
		}
		return err
	}

	// Ensure map is initialized
	if progress.ViewedPages == nil {
		progress.ViewedPages = make(map[string]bool)
	}

	m.progress = &progress
	m.dirty = false
	return nil
}

// Save writes tutorial progress to disk. With BV_NO_SAVED_CONFIG set nothing
// is written and the in-memory state stays dirty.
func (m *tutorialProgressManager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.dirty || savedConfigDisabled() {
		return nil // Nothing to save, or persistence disabled
	}

	path := TutorialProgressPath()
	if path == "" {
		return nil // Can't determine path
	}

	// Ensure directory exists
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Update last viewed time
	m.progress.LastViewedTime = time.Now()

	data, err := json.MarshalIndent(m.progress, "", "  ")
	if err != nil {
		return err
	}

	// Write atomically via temp file
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // Clean up
		return err
	}

	m.dirty = false
	return nil
}

// MarkPageViewed marks a page as viewed and makes it the resume point. The
// resume point moves even for pages that were already viewed, so reopening
// the tutorial lands on the page the user actually left.
func (m *tutorialProgressManager) MarkPageViewed(pageID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if pageID == "" {
		return
	}
	if !m.progress.ViewedPages[pageID] {
		m.progress.ViewedPages[pageID] = true
		m.dirty = true
	}
	if m.progress.LastPageID != pageID {
		m.progress.LastPageID = pageID
		m.dirty = true
	}
}

// IsPageViewed returns whether a page has been viewed.
func (m *tutorialProgressManager) IsPageViewed(pageID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress.ViewedPages[pageID]
}

// GetViewedCount returns the number of pages viewed.
func (m *tutorialProgressManager) GetViewedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	for _, viewed := range m.progress.ViewedPages {
		if viewed {
			count++
		}
	}
	return count
}

// GetLastPageID returns the last page viewed.
func (m *tutorialProgressManager) GetLastPageID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress.LastPageID
}

// SetCompletedOnce marks that the user has completed the tutorial at least once.
func (m *tutorialProgressManager) SetCompletedOnce() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.progress.CompletedOnce {
		m.progress.CompletedOnce = true
		m.dirty = true
	}
}

// HasCompletedOnce returns whether the user has completed the tutorial.
func (m *tutorialProgressManager) HasCompletedOnce() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress.CompletedOnce
}

// Reset clears all progress (for testing or user reset).
func (m *tutorialProgressManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.progress = &TutorialProgress{
		ViewedPages: make(map[string]bool),
	}
	m.dirty = true
}

// IsDirty returns whether there are unsaved changes.
func (m *tutorialProgressManager) IsDirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dirty
}

// GetProgress returns a copy of the current progress.
func (m *tutorialProgressManager) GetProgress() TutorialProgress {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return a copy to avoid race conditions
	viewedCopy := make(map[string]bool)
	for k, v := range m.progress.ViewedPages {
		viewedCopy[k] = v
	}

	return TutorialProgress{
		ViewedPages:    viewedCopy,
		LastPageID:     m.progress.LastPageID,
		LastViewedTime: m.progress.LastViewedTime,
		CompletedOnce:  m.progress.CompletedOnce,
	}
}

// Integration methods for TutorialModel

// SaveProgress saves current tutorial progress (call on exit).
func (m *TutorialModel) SaveProgress() error {
	pm := GetTutorialProgressManager()

	// Mark current page as viewed
	if m.currentPage >= 0 && m.currentPage < len(m.pages) {
		pm.MarkPageViewed(m.pages[m.currentPage].ID)
	}

	// Check if all pages have been viewed
	allViewed := true
	for _, page := range m.pages {
		if !pm.IsPageViewed(page.ID) {
			allViewed = false
			break
		}
	}
	if allViewed && len(m.pages) > 0 {
		pm.SetCompletedOnce()
	}

	return pm.Save()
}

// LoadProgress loads saved progress into the tutorial model.
func (m *TutorialModel) LoadProgress() {
	pm := GetTutorialProgressManager()

	// Update progress map (local session tracking)
	for _, page := range m.pages {
		if pm.IsPageViewed(page.ID) {
			m.progress[page.ID] = true
		}
	}
}

// ResumeLastPage moves to the persisted resume point, if it names a page that
// is currently visible. Returns true when the page changed.
func (m *TutorialModel) ResumeLastPage() bool {
	lastID := GetTutorialProgressManager().GetLastPageID()
	if lastID == "" {
		return false
	}
	for i, page := range m.visiblePages() {
		if page.ID == lastID {
			if i == m.currentPage {
				return false
			}
			m.currentPage = i
			m.scrollOffset = 0
			m.tocCursor = i
			return true
		}
	}
	return false
}

// markCurrentPageViewed records the current page both in the session map and
// in the persisted progress so every page change updates the resume point.
func (m *TutorialModel) markCurrentPageViewed() {
	pages := m.visiblePages()
	if m.currentPage < 0 || m.currentPage >= len(pages) {
		return
	}
	id := pages[m.currentPage].ID
	m.progress[id] = true
	GetTutorialProgressManager().MarkPageViewed(id)
}

// HasViewedPage returns whether a page has been viewed (from persisted data).
func (m *TutorialModel) HasViewedPage(pageID string) bool {
	// Check local state first (for current session)
	if m.progress[pageID] {
		return true
	}
	// Check persisted state
	pm := GetTutorialProgressManager()
	return pm.IsPageViewed(pageID)
}
