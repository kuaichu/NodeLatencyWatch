package controller

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
	"node-latency-watch/internal/model"
)

type Store struct {
	db *sql.DB
}

func OpenStore(stateDir string) (*Store, error) {
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(stateDir, "runtime.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;`); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS node_samples (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	time TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	agent_name TEXT NOT NULL,
	carrier TEXT NOT NULL,
	carrier_label TEXT NOT NULL,
	probe_source TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	provider TEXT NOT NULL,
	category TEXT NOT NULL DEFAULT '',
	node_id TEXT NOT NULL,
	node_name TEXT NOT NULL,
	protocol TEXT NOT NULL,
	server TEXT NOT NULL,
	port INTEGER NOT NULL DEFAULT 0,
	dns_ms REAL NOT NULL DEFAULT 0,
	tcp_ms REAL NOT NULL DEFAULT 0,
	tls_ms REAL NOT NULL DEFAULT 0,
	max_rtt_ms REAL NOT NULL DEFAULT 0,
	rtt_stddev_ms REAL NOT NULL DEFAULT 0,
	http_ms REAL NOT NULL DEFAULT 0,
	attempts INTEGER NOT NULL DEFAULT 0,
	successes INTEGER NOT NULL DEFAULT 0,
	loss_rate REAL NOT NULL DEFAULT 0,
	success INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	resolved_ip TEXT NOT NULL DEFAULT '',
	probe_mode TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS agent_reports (
	agent_id TEXT PRIMARY KEY,
	finished_at TEXT NOT NULL,
	report_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_node_samples_time ON node_samples(time);
CREATE INDEX IF NOT EXISTS idx_node_samples_node_time ON node_samples(node_id, time);
CREATE INDEX IF NOT EXISTS idx_node_samples_agent_time ON node_samples(agent_id, time);
CREATE INDEX IF NOT EXISTS idx_agent_reports_finished ON agent_reports(finished_at);
`)
	if err != nil {
		return err
	}
	_, _ = s.db.Exec(`ALTER TABLE node_samples ADD COLUMN category TEXT NOT NULL DEFAULT ''`)
	_, _ = s.db.Exec(`ALTER TABLE node_samples ADD COLUMN max_rtt_ms REAL NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE node_samples ADD COLUMN rtt_stddev_ms REAL NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE node_samples ADD COLUMN http_ms REAL NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE node_samples ADD COLUMN probe_mode TEXT NOT NULL DEFAULT ''`)
	return err
}

func (s *Store) InsertSamples(samples []model.NodeSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO node_samples(
		time, agent_id, agent_name, carrier, carrier_label, probe_source,
		provider_id, provider, category, node_id, node_name, protocol, server, port,
		dns_ms, tcp_ms, tls_ms, max_rtt_ms, rtt_stddev_ms, http_ms, attempts, successes, loss_rate, success, error, resolved_ip, probe_mode
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, sample := range samples {
		success := 0
		if sample.Success {
			success = 1
		}
		if _, err := stmt.Exec(
			timeText(sample.Time), sample.AgentID, sample.AgentName, sample.Carrier, sample.CarrierLabel, sample.ProbeSource,
			sample.ProviderID, sample.Provider, sample.Category, sample.NodeID, sample.NodeName, sample.Protocol, sample.Server, sample.Port,
			sample.DNSMs, sample.TCPMs, sample.TLSMs, sample.MaxRTTMs, sample.RTTStdDevMs, sample.HTTPMs,
			sample.Attempts, sample.Successes, sample.LossRate, success, sample.Error, sample.ResolvedIP, sample.ProbeMode,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LatestSamples(limit int) ([]model.NodeSample, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT time, agent_id, agent_name, carrier, carrier_label, probe_source, provider_id, provider, category, node_id, node_name, protocol, server, port, dns_ms, tcp_ms, tls_ms, max_rtt_ms, rtt_stddev_ms, http_ms, attempts, successes, loss_rate, success, error, resolved_ip, probe_mode
		FROM node_samples ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var samples []model.NodeSample
	for rows.Next() {
		sample, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

func (s *Store) SamplesForNode(nodeID string, since time.Time, limit int) ([]model.NodeSample, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(`SELECT time, agent_id, agent_name, carrier, carrier_label, probe_source, provider_id, provider, category, node_id, node_name, protocol, server, port, dns_ms, tcp_ms, tls_ms, max_rtt_ms, rtt_stddev_ms, http_ms, attempts, successes, loss_rate, success, error, resolved_ip, probe_mode
		FROM (
			SELECT time, agent_id, agent_name, carrier, carrier_label, probe_source, provider_id, provider, category, node_id, node_name, protocol, server, port, dns_ms, tcp_ms, tls_ms, max_rtt_ms, rtt_stddev_ms, http_ms, attempts, successes, loss_rate, success, error, resolved_ip, probe_mode
			FROM node_samples WHERE node_id = ? AND time >= ? ORDER BY time DESC LIMIT ?
		) ORDER BY time ASC`, nodeID, timeText(since), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var samples []model.NodeSample
	for rows.Next() {
		sample, err := scanSample(rows)
		if err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	return samples, rows.Err()
}

func (s *Store) UpsertAgentReport(report model.AgentReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO agent_reports(agent_id, finished_at, report_json) VALUES(?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET finished_at = excluded.finished_at, report_json = excluded.report_json`,
		report.AgentID, timeText(report.FinishedAt), string(data))
	return err
}

func (s *Store) DeleteAgentReport(agentID string) error {
	_, err := s.db.Exec(`DELETE FROM agent_reports WHERE agent_id = ?`, agentID)
	return err
}

func (s *Store) AgentReports(ttl time.Duration) ([]model.AgentReport, error) {
	query := `SELECT report_json FROM agent_reports`
	args := []any{}
	if ttl > 0 {
		query += ` WHERE finished_at >= ?`
		args = append(args, timeText(time.Now().Add(-ttl)))
	}
	query += ` ORDER BY finished_at DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var reports []model.AgentReport
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var report model.AgentReport
		if err := json.Unmarshal([]byte(raw), &report); err == nil {
			reports = append(reports, report)
		}
	}
	return reports, rows.Err()
}

func scanSample(rows interface {
	Scan(dest ...any) error
}) (model.NodeSample, error) {
	var sample model.NodeSample
	var timeRaw string
	var success int
	err := rows.Scan(&timeRaw, &sample.AgentID, &sample.AgentName, &sample.Carrier, &sample.CarrierLabel, &sample.ProbeSource,
		&sample.ProviderID, &sample.Provider, &sample.Category, &sample.NodeID, &sample.NodeName, &sample.Protocol, &sample.Server, &sample.Port,
		&sample.DNSMs, &sample.TCPMs, &sample.TLSMs, &sample.MaxRTTMs, &sample.RTTStdDevMs, &sample.HTTPMs,
		&sample.Attempts, &sample.Successes, &sample.LossRate, &success, &sample.Error, &sample.ResolvedIP, &sample.ProbeMode)
	if err != nil {
		return sample, err
	}
	sample.Time = parseTime(timeRaw)
	sample.Success = success == 1
	return sample, nil
}

func timeText(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}
