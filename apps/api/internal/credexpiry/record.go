package credexpiry

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/sinaneysi/argus/internal/incidents"
)

// AlertText is the incident title and severity of an alert band.
func AlertText(it *Item) (string, string) {
	switch it.Status {
	case "rejected":
		return "Credential rejected: " + it.Name, "critical"
	case "expired":
		return "Credential expired: " + it.Name, "critical"
	}
	d := 0
	if it.DaysLeft != nil {
		d = *it.DaysLeft
	}
	return fmt.Sprintf("Credential expires in %d d: %s", d, it.Name), "high"
}

// Notice is one notice to send: the credential and its incident (0 when none).
type Notice struct {
	Item     *Item
	Incident int64
}

// Result is what one record did.
type Result struct {
	Incidents []int64
	Notices   []Notice
	Resolved  []int64
}

var rank = map[string]int{"crit": 1, "expired": 2, "rejected": 2}

type alertRow struct {
	band               string
	since              int64
	notifiedBand       sql.NullString
	notifiedAt         sql.NullInt64
	incident, attached sql.NullInt64
}

// Record raises and resolves from a credentials.json document: an incident per alert band (again after 20 h while
// it lasts), one notice when the band gets worse, the incident resolved when the credential is OK again (not on a
// failed check). io holds the incident options (actor, ...).
func Record(db *sql.DB, doc *Doc, now int64, io *incidents.Options) (Result, error) {
	var res Result
	if doc == nil {
		return res, errors.New("no credentials.json yet")
	}
	tx, err := db.Begin()
	if err != nil {
		return res, err
	}
	defer tx.Rollback()
	actor := io.Actor
	if actor == "" {
		actor = "argus"
	}
	for _, it := range doc.Items {
		var p *alertRow
		r := alertRow{}
		err := tx.QueryRow(`SELECT band, since, notified_band, notified_at, incident_id, attached_at FROM credential_alerts WHERE id=?`, it.ID).
			Scan(&r.band, &r.since, &r.notifiedBand, &r.notifiedAt, &r.incident, &r.attached)
		if err == nil {
			p = &r
		} else if !errors.Is(err, sql.ErrNoRows) {
			return res, err
		}
		st := it.Status
		if AlertBands[st] {
			title, sev := AlertText(it)
			var iid sql.NullInt64
			if p != nil {
				iid = p.incident
			}
			fresh := p == nil || p.band != st || !p.attached.Valid || p.attached.Int64 == 0 || now-p.attached.Int64 >= 20*3600
			if fresh {
				text := fmt.Sprintf("%s (%s): %s. Used by %s. Rotate it: Settings → Credentials.", it.Name, it.KindLabel, it.Detail, it.UsedBy)
				id, err := io.Attach(tx, incidents.Finding{Task: "cred", Title: title, Severity: sev, Env: "", Text: text, At: now},
					[]incidents.Cand{{DC: "CRED", Subject: it.ID, Family: "cred-expiry"}})
				if err != nil {
					return res, err
				}
				iid = sql.NullInt64{Int64: id, Valid: id != 0}
				res.Incidents = append(res.Incidents, id)
			}
			notify := p == nil || rank[p.notifiedBand.String] < rank[st]
			since := now
			if p != nil && p.band == st {
				since = p.since
			}
			nb, na := sql.NullString{String: st, Valid: true}, sql.NullInt64{Int64: now, Valid: true}
			if !notify {
				nb, na = sql.NullString{}, sql.NullInt64{}
				if p != nil {
					nb, na = p.notifiedBand, p.notifiedAt
				}
			}
			att := sql.NullInt64{Int64: now, Valid: true}
			if !fresh {
				att = p.attached
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO credential_alerts (id, band, since, notified_band, notified_at, incident_id, attached_at)
 VALUES (?,?,?,?,?,?,?)`, it.ID, st, since, nb, na, iid, att); err != nil {
				return res, err
			}
			if notify {
				res.Notices = append(res.Notices, Notice{it, iid.Int64})
			}
		} else if p != nil {
			if p.incident.Valid && st != "error" {
				var status string
				if tx.QueryRow(`SELECT status FROM incidents WHERE id=?`, p.incident.Int64).Scan(&status) == nil && (status == "open" || status == "acknowledged") {
					note := clip("credential OK again: "+it.Detail, 300)
					if _, err := tx.Exec(`UPDATE incidents SET status='resolved', resolved_at=?, resolved_by=?, resolve_note=? WHERE id=?`,
						now, actor, note, p.incident.Int64); err != nil {
						return res, err
					}
					if _, err := tx.Exec(`INSERT INTO incident_events (incident_id, at, kind, actor, text) VALUES (?,?,?,?,?)`,
						p.incident.Int64, now, "auto-resolve", actor, note); err != nil {
						return res, err
					}
					res.Resolved = append(res.Resolved, p.incident.Int64)
				}
			}
			if st != "error" {
				if _, err := tx.Exec(`DELETE FROM credential_alerts WHERE id=?`, it.ID); err != nil {
					return res, err
				}
			}
		}
	}
	return res, tx.Commit()
}
