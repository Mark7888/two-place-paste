package update

import (
	"context"

	"github.com/Mark7888/two-place-paste/desktop/internal/localui"
)

// Manager is the localhost UI's updater.
var _ localui.UpdateAPI = (*Manager)(nil)

// UpdateStatus implements localui.UpdateAPI.
func (m *Manager) UpdateStatus(ctx context.Context) localui.UpdateView { return m.Status(ctx) }

// CheckUpdate implements localui.UpdateAPI.
func (m *Manager) CheckUpdate(ctx context.Context) (localui.UpdateView, error) { return m.Check(ctx) }

// InstallUpdate implements localui.UpdateAPI.
func (m *Manager) InstallUpdate(ctx context.Context, req localui.InstallRequest) (localui.UpdateView, error) {
	return m.Install(ctx, req)
}

// RollbackUpdate implements localui.UpdateAPI.
func (m *Manager) RollbackUpdate(ctx context.Context) (localui.UpdateView, error) {
	return m.Rollback(ctx)
}
