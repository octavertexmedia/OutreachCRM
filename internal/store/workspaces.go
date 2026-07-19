package store

import (
	"fmt"
	"strings"

	"github.com/manishkumar/outreachcrm/internal/models"
)

const (
	WorkspaceOctaVertex = "OctaVertex Media"
	WorkspaceRevNext    = "RevNext"
	WorkspaceDefault    = "Default"

	PlaybookOVM     = "ovm"
	PlaybookRevNext = "revnext"
	PlaybookAll     = "all"
	PlaybookNone    = "none"
)

// EnsureNamedWorkspace returns an existing workspace by name (case-insensitive) or creates it.
func (s *Store) EnsureNamedWorkspace(name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = WorkspaceDefault
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM workspaces WHERE lower(name)=lower(?) LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	res, err := s.db.Exec(`INSERT INTO workspaces(name, created_at) VALUES(?,?)`, name, fmtTime(now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EnsureBrandWorkspaces guarantees OctaVertex Media and RevNext tenants exist.
func (s *Store) EnsureBrandWorkspaces() (ovmID, revID int64, err error) {
	// Keep a Default workspace for platform/ops if none exist yet.
	if _, err = s.EnsureNamedWorkspace(WorkspaceDefault); err != nil {
		return 0, 0, err
	}
	ovmID, err = s.EnsureNamedWorkspace(WorkspaceOctaVertex)
	if err != nil {
		return 0, 0, err
	}
	revID, err = s.EnsureNamedWorkspace(WorkspaceRevNext)
	if err != nil {
		return 0, 0, err
	}
	return ovmID, revID, nil
}

func (s *Store) GetWorkspace(id int64) (models.Workspace, error) {
	var w models.Workspace
	var created string
	err := s.db.QueryRow(`SELECT id, name, created_at FROM workspaces WHERE id=?`, id).Scan(&w.ID, &w.Name, &created)
	if err != nil {
		return w, err
	}
	w.CreatedAt = parseTime(created)
	return w, nil
}

func (s *Store) WorkspaceName(id int64) string {
	if id <= 0 {
		return WorkspaceDefault
	}
	w, err := s.GetWorkspace(id)
	if err != nil {
		return fmt.Sprintf("Workspace %d", id)
	}
	return w.Name
}

// WorkspaceStats counts core entities for the workspaces admin table.
func (s *Store) WorkspaceStats(id int64) (leads, campaigns, users, templates, audiences int) {
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM leads WHERE workspace_id=?`, id).Scan(&leads)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM campaigns WHERE workspace_id=?`, id).Scan(&campaigns)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE workspace_id=?`, id).Scan(&users)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM email_templates WHERE workspace_id=?`, id).Scan(&templates)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM audiences WHERE workspace_id=?`, id).Scan(&audiences)
	return
}

func (s *Store) SetUserWorkspace(userID, workspaceID int64) error {
	if workspaceID <= 0 {
		return fmt.Errorf("invalid workspace")
	}
	if _, err := s.GetWorkspace(workspaceID); err != nil {
		return fmt.Errorf("workspace not found")
	}
	_, err := s.db.Exec(`UPDATE users SET workspace_id=? WHERE id=?`, workspaceID, userID)
	return err
}

// OnboardWorkspace creates a named workspace and optionally seeds a playbook pack.
func (s *Store) OnboardWorkspace(name, playbook string, ownerID int64) (wsID int64, templates, campaigns int, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, 0, 0, fmt.Errorf("name required")
	}
	wsID, err = s.EnsureNamedWorkspace(name)
	if err != nil {
		return 0, 0, 0, err
	}
	playbook = strings.ToLower(strings.TrimSpace(playbook))
	switch playbook {
	case PlaybookOVM, PlaybookRevNext, PlaybookAll:
		_, templates, campaigns, err = s.SeedCompanyPlaybooksFor(ownerID, wsID, playbook)
	case PlaybookNone, "":
		// blank tenant
	default:
		return wsID, 0, 0, fmt.Errorf("unknown playbook pack %q", playbook)
	}
	return wsID, templates, campaigns, err
}

// SeedBrandPlaybooks seeds OVM packs into OctaVertex Media and RevNext packs into RevNext.
func (s *Store) SeedBrandPlaybooks(ownerID int64) (ovmT, ovmC, revT, revC, purged int, err error) {
	ovmID, revID, err := s.EnsureBrandWorkspaces()
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	purged, ovmT, ovmC, err = s.SeedCompanyPlaybooksFor(ownerID, ovmID, PlaybookOVM)
	if err != nil {
		return
	}
	_, revT, revC, err = s.SeedCompanyPlaybooksFor(ownerID, revID, PlaybookRevNext)
	return
}
