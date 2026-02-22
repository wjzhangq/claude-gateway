package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/claude-gateway/claude-gateway/internal/model"
)

func (d *DB) CreateApplication(a *model.Application) error {
	now := time.Now()
	res, err := d.Exec(
		`INSERT INTO applications (user_id, model, reason, status, created_at, updated_at)
		 VALUES (?, ?, ?, 'pending', ?, ?)`,
		a.UserID, a.Model, a.Reason, now, now,
	)
	if err != nil {
		return fmt.Errorf("create application: %w", err)
	}
	a.ID, _ = res.LastInsertId()
	a.Status = "pending"
	a.CreatedAt = now
	a.UpdatedAt = now
	return nil
}

func (d *DB) GetApplicationByID(id int64) (*model.Application, error) {
	a := &model.Application{}
	err := d.QueryRow(
		`SELECT id, user_id, model, reason, status, reviewer_id, review_note, created_at, updated_at
		 FROM applications WHERE id = ?`, id,
	).Scan(&a.ID, &a.UserID, &a.Model, &a.Reason, &a.Status,
		&a.ReviewerID, &a.ReviewNote, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return a, err
}

func (d *DB) ListApplications(userID int64, status string) ([]*model.Application, error) {
	where := "WHERE 1=1"
	args := []interface{}{}
	if userID > 0 {
		where += " AND user_id = ?"
		args = append(args, userID)
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}
	rows, err := d.Query(
		`SELECT id, user_id, model, reason, status, reviewer_id, review_note, created_at, updated_at
		 FROM applications `+where+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var apps []*model.Application
	for rows.Next() {
		a := &model.Application{}
		if err := rows.Scan(&a.ID, &a.UserID, &a.Model, &a.Reason, &a.Status,
			&a.ReviewerID, &a.ReviewNote, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		apps = append(apps, a)
	}
	return apps, rows.Err()
}

func (d *DB) ReviewApplication(id, reviewerID int64, status, note string) error {
	_, err := d.Exec(
		`UPDATE applications SET status=?, reviewer_id=?, review_note=?, updated_at=? WHERE id=?`,
		status, reviewerID, note, time.Now(), id,
	)
	return err
}
