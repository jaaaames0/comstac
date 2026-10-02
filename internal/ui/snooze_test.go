package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"comstac/internal/store"
)

func sydney(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.ParseInLocation("2006-01-02 15:04", value, sydneyLoc)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestSnoozePresets(t *testing.T) {
	cases := []struct {
		name string
		now  string
		want []string // "label@Sydney time"
	}{
		{
			name: "weekday morning",
			now:  "2026-10-07 10:00", // Wednesday
			want: []string{"later today@2026-10-07 18:00", "tomorrow@2026-10-08 08:00", "this weekend@2026-10-10 08:00", "next week@2026-10-12 08:00"},
		},
		{
			name: "friday evening drops duplicate weekend",
			now:  "2026-10-09 20:00", // Friday, on the hour: +3h exactly
			want: []string{"later today@2026-10-09 23:00", "tomorrow@2026-10-10 08:00", "next week@2026-10-12 08:00"},
		},
		{
			name: "late night rolls past midnight",
			now:  "2026-10-07 22:30",
			want: []string{"later@2026-10-08 02:00", "tomorrow@2026-10-08 08:00", "this weekend@2026-10-10 08:00", "next week@2026-10-12 08:00"},
		},
		{
			name: "saturday offers next weekend; dst start keeps 08:00 local",
			now:  "2026-10-03 09:00", // Saturday before DST begins on Sunday 4 Oct
			want: []string{"later today@2026-10-03 18:00", "tomorrow@2026-10-04 08:00", "next weekend@2026-10-10 08:00", "next week@2026-10-05 08:00"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := snoozePresets(sydney(t, tc.now))
			var labels []string
			for _, p := range got {
				until, err := time.Parse(time.RFC3339, p.Until)
				if err != nil {
					t.Fatalf("bad until %q: %v", p.Until, err)
				}
				labels = append(labels, p.Label+"@"+until.In(sydneyLoc).Format("2006-01-02 15:04"))
			}
			if strings.Join(labels, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("presets\n got %v\nwant %v", labels, tc.want)
			}
		})
	}
}

func TestParseSnoozeUntil(t *testing.T) {
	now := sydney(t, "2026-10-07 10:00")

	got, err := parseSnoozeUntil("2026-10-07T18:30", now)
	if err != nil || !got.Equal(sydney(t, "2026-10-07 18:30")) || got.Location() != time.UTC {
		t.Fatalf("datetime-local not read as Sydney UTC instant: %v %v", got, err)
	}
	if _, err := parseSnoozeUntil("2026-10-08T00:00:00Z", now); err != nil {
		t.Fatalf("rfc3339 rejected: %v", err)
	}
	for _, bad := range []string{"", "tomorrow", "2026-10-07T09:59", "2027-10-09T10:00"} {
		if _, err := parseSnoozeUntil(bad, now); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

func TestMessageDetailSnoozeMenuAndBadges(t *testing.T) {
	var rendered bytes.Buffer
	execute(&rendered, "message_detail", newMessageDetailData(&store.MessageDetail{ID: 9}, false))
	out := rendered.String()
	if !strings.Contains(out, `"action":"snooze","until":"`) || !strings.Contains(out, `type="datetime-local" name="until"`) {
		t.Fatalf("snooze presets or custom picker missing: %s", out)
	}

	rendered.Reset()
	execute(&rendered, "message_detail", newMessageDetailData(&store.MessageDetail{ID: 9, SnoozeUntil: "2026-10-07T21:00:00Z"}, false))
	if !strings.Contains(rendered.String(), "snoozed until Thu 8 Oct 08:00") || !strings.Contains(rendered.String(), ">unsnooze</button>") {
		t.Fatalf("snoozed state not rendered in Sydney time: %s", rendered.String())
	}

	rendered.Reset()
	execute(&rendered, "message_list", newMessageListData([]store.MessageListItem{
		{ID: 1, SnoozeUntil: "2026-10-07T21:00:00Z"},
		{ID: 2, ResurfacedAt: "2026-10-07 21:00:00"},
		{ID: 3, ResurfacedAt: "2026-10-07 21:00:00", Read: true},
	}, store.ListMessageOptions{Limit: 3, Snoozed: true}))
	list := rendered.String()
	if !strings.Contains(list, "snoozed · Thu 8 Oct 08:00") || strings.Count(list, ">reminder</span>") != 1 {
		t.Fatalf("list badges wrong: %s", list)
	}
	if strings.Contains(list, "msg-more-sentinel") {
		t.Fatal("snoozed view must not paginate")
	}
}
