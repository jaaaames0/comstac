package ui

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"

	"comstac/internal/store"
)

type contactsData struct {
	Items                         []store.Contact
	Query, Previous, Next, Notice string
}
type contactFormData struct {
	Contact store.Contact
	Error   string
}

func registerContactRoutes(mux *http.ServeMux, db *sql.DB) {
	mux.HandleFunc("GET /ui/contacts", func(w http.ResponseWriter, r *http.Request) { renderContacts(w, r, db, "") })
	mux.HandleFunc("GET /ui/contacts/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		items, err := store.SearchContacts(r.Context(), db, q, 10, 0)
		if err != nil {
			http.Error(w, "invalid contact search", http.StatusBadRequest)
			return
		}
		type suggestion struct {
			Name      string `json:"name"`
			Email     string `json:"email"`
			Recipient string `json:"recipient"`
		}
		out := make([]suggestion, 0, len(items))
		for _, c := range items {
			out = append(out, suggestion{c.Name, c.Email, c.Recipient()})
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("GET /ui/contacts/edit", func(w http.ResponseWriter, r *http.Request) {
		c := store.Contact{}
		if raw := r.URL.Query().Get("id"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "invalid contact", http.StatusBadRequest)
				return
			}
			got, err := store.GetContact(r.Context(), db, id)
			if err != nil {
				contactReadError(w, err)
				return
			}
			c = *got
		} else if raw := r.URL.Query().Get("message"); raw != "" {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				http.Error(w, "invalid message", http.StatusBadRequest)
				return
			}
			sender, err := store.GetMessageSender(r.Context(), db, id)
			if err != nil {
				contactReadError(w, err)
				return
			}
			a, err := mail.ParseAddress(sender)
			if err != nil {
				renderContactForm(w, contactFormData{Error: "This sender could not be parsed. Enter the contact details below."})
				return
			}
			c.Name, c.Email = a.Name, a.Address
		}
		renderContactForm(w, contactFormData{Contact: c})
	})
	mux.HandleFunc("POST /ui/contacts/save", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid contact form", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(r.Form.Get("id"), 10, 64)
		if err != nil || id < 0 {
			http.Error(w, "invalid contact", http.StatusBadRequest)
			return
		}
		c := store.Contact{ID: id, Name: r.Form.Get("name"), Email: r.Form.Get("email")}
		if err := c.Validate(); err != nil {
			renderContactForm(w, contactFormData{Contact: c, Error: err.Error()})
			return
		}
		if err := store.SaveContact(r.Context(), db, &c); err != nil {
			if errors.Is(err, store.ErrDuplicateContact) {
				renderContactForm(w, contactFormData{Contact: c, Error: err.Error()})
				return
			}
			contactReadError(w, err)
			return
		}
		// Mutation forms contain no list query: return the first page.
		r.URL.RawQuery = ""
		renderContacts(w, r, db, "Contact saved.")
	})
	mux.HandleFunc("POST /ui/contacts/delete", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid contact form", http.StatusBadRequest)
			return
		}
		id, err := strconv.ParseInt(r.Form.Get("id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "invalid contact", http.StatusBadRequest)
			return
		}
		if err := store.DeleteContact(r.Context(), db, id); err != nil {
			contactReadError(w, err)
			return
		}
		r.URL.RawQuery = ""
		renderContacts(w, r, db, "Contact deleted.")
	})
}

func contactReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrContactNotFound) || errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, "could not access contacts", http.StatusInternalServerError)
}
func renderContactForm(w http.ResponseWriter, d contactFormData) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	execute(w, "contact_form", d)
}
func renderContacts(w http.ResponseWriter, r *http.Request, db *sql.DB, notice string) {
	q := r.URL.Query().Get("q")
	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		var err error
		page, err = strconv.Atoi(raw)
		if err != nil || page < 0 || page > 20000 {
			http.Error(w, "invalid contact page", http.StatusBadRequest)
			return
		}
	}
	items, err := store.SearchContacts(r.Context(), db, q, 51, page*50)
	if err != nil {
		http.Error(w, "could not search contacts", http.StatusBadRequest)
		return
	}
	d := contactsData{Items: items, Query: q, Notice: notice}
	pageURL := func(p int) string { return "/ui/contacts?" + url.Values{"q": {q}, "page": {strconv.Itoa(p)}}.Encode() }
	if len(items) > 50 {
		d.Items = items[:50]
		d.Next = pageURL(page + 1)
	}
	if page > 0 {
		d.Previous = pageURL(page - 1)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	execute(w, "contacts", d)
}
