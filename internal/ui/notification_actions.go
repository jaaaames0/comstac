package ui

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"comstac/internal/calendar"
	"comstac/internal/store"
)

// These routes are behind the normal session and CSRF middleware. The worker
// gets a fresh token using its session cookie without requiring an open page.
func registerNotificationActionRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("/api/push/action-token", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"csrf_token": CSRFToken(req.Context())})
	})
	mux.HandleFunc("/api/push/action", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if db == nil {
			http.Error(w, "database not configured", http.StatusServiceUnavailable)
			return
		}
		var data struct {
			Action     string `json:"action"`
			MessageID  int64  `json:"message_id"`
			EventID    int64  `json:"event_id"`
			Occurrence string `json:"occurrence"`
			SourceKey  string `json:"source_key"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&data); err != nil {
			http.Error(w, "invalid notification action", http.StatusBadRequest)
			return
		}
		var applied bool
		var err error
		switch data.Action {
		case "trash", "read", "snooze_mail":
			if data.MessageID <= 0 || data.EventID != 0 {
				http.Error(w, "invalid message", http.StatusBadRequest)
				return
			}
			applied, err = store.ApplyNotificationMessageAction(req.Context(), db, data.MessageID, data.Action, time.Now())
		case "snooze_event":
			start, parseErr := time.Parse(time.RFC3339, data.Occurrence)
			if data.EventID <= 0 || data.MessageID != 0 || parseErr != nil {
				http.Error(w, "invalid occurrence", http.StatusBadRequest)
				return
			}
			var ev *store.CalendarEvent
			ev, err = store.GetCalendarEvent(req.Context(), db, data.EventID)
			if err == nil && ev != nil && ev.Status == store.CalendarConfirmed {
				var event *calendar.Event
				event, err = calendar.NewEvent(ev.ID, ev.Title, ev.Kind, ev.Location, ev.AllDay, ev.StartLocal, ev.EndLocal, ev.TZ, ev.RRule, ev.ExDates)
				if err == nil {
					for _, occ := range event.Occurrences(start, start.Add(time.Second)) {
						if occ.Start.Equal(start) {
							applied, err = store.SnoozeCalendarNotification(req.Context(), db, ev.ID, start, data.SourceKey, time.Now())
							break
						}
					}
				}
			}
		default:
			http.Error(w, "invalid notification action", http.StatusBadRequest)
			return
		}
		if err != nil {
			slog.Error("notification action failed", "action", data.Action, "err", err)
			http.Error(w, "notification action failed", http.StatusInternalServerError)
			return
		}
		if !applied {
			http.Error(w, "message or reminder is no longer available", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
