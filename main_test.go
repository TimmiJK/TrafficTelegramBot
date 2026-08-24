package main

import (
	"math"
	"testing"
	"time"
)

func TestConvertBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1098576, "1.0 MB"},
		{1158576, "1.1 MB"},
		{5368709120, "5.0 GB"},
		{1099511627776, "1.0 TB"},
		{1125899906842624, "1.0 PB"},
	}
	for _, c := range cases {
		if got := ConvertBytes(c.in); got != c.want {
			t.Errorf("ConvertBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestConvertBytesCorner(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{-1, "-1 B"},
		{-2048, "-2048 B"},
		{1, "1 B"},
		{1025, "1.0 KB"},
		{1076, "1.1 KB"},
		{1073741824, "1.0 GB"},
		{math.MaxInt64, "8.0 EB"},
	}
	for _, c := range cases {
		if got := ConvertBytes(c.in); got != c.want {
			t.Errorf("ConvertBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDaysInMonth(t *testing.T) {
	cases := []struct {
		y    int
		m    time.Month
		want int
	}{
		{2026, 1, 31},
		{2026, 2, 28},
		{2024, 2, 29},
		{2026, 4, 30},
		{2023, 7, 31},
		{2026, 12, 31},
	}
	for _, c := range cases {
		if got := daysInMonth(c.y, c.m); got != c.want {
			t.Errorf("daysInMonth(%d, %d) = %d, want %d", c.y, c.m, got, c.want)
		}
	}
}

func TestBillingStartClamp(t *testing.T) {
	loc := time.UTC

	got := billingStart(2026, 2, 29, loc)
	want := time.Date(2026, 2, 28, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("billingStart(2026,2,29) = %v, want %v", got, want)
	}

	got = billingStart(2024, 2, 29, loc)
	want = time.Date(2024, 2, 29, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("billingStart(2024,2,29) = %v, want %v", got, want)
	}

	got = billingStart(2026, 3, 29, loc)
	want = time.Date(2026, 3, 29, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("billingStart(2026,3,29) = %v, want %v", got, want)
	}

	got = billingStart(2026, 6, 31, loc)
	want = time.Date(2026, 6, 30, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Errorf("billingStart(2026,6,31) = %v, want %v", got, want)
	}
}

func TestBillingBoundsRolling(t *testing.T) {
	loc := time.UTC
	billingStart0 = time.Date(2026, 7, 30, 0, 0, 0, 0, loc)
	billingEnd0 = time.Date(2026, 8, 29, 0, 0, 0, 0, loc)
	t.Cleanup(func() { billingStart0 = time.Time{}; billingEnd0 = time.Time{} })

	if got := billingStartAt(1, loc); !got.Equal(time.Date(2026, 8, 30, 0, 0, 0, 0, loc)) {
		t.Errorf("start(1) = %v, want 2026-08-30", got)
	}
	if got := billingStartAt(2, loc); !got.Equal(time.Date(2026, 9, 29, 0, 0, 0, 0, loc)) {
		t.Errorf("start(2) = %v, want 2026-09-29", got)
	}

	now := time.Date(2026, 8, 24, 0, 0, 0, 0, loc)
	start, end := billingBounds(now, 0, loc)
	if got := time.Unix(start, 0).In(loc); !got.Equal(billingStart0) {
		t.Errorf("current start = %v, want %v", got, billingStart0)
	}
	if end != now.Unix()+1 {
		t.Errorf("current end must equal now")
	}

	_, prevEnd := billingBounds(now, 1, loc)
	if prevEnd != billingStart0.Unix() {
		t.Errorf("prev end = %d, want %d", prevEnd, billingStart0.Unix())
	}

	future := time.Date(2026, 9, 5, 0, 0, 0, 0, loc)
	fStart, _ := billingBounds(future, 0, loc)
	if got := time.Unix(fStart, 0).In(loc); !got.Equal(time.Date(2026, 8, 30, 0, 0, 0, 0, loc)) {
		t.Errorf("sept current start = %v, want 2026-08-30", got)
	}

	future2 := time.Date(2027, 1, 7, 0, 0, 0, 0, loc)
	fStart2, _ := billingBounds(future2, 0, loc)
	if got := time.Unix(fStart2, 0).In(loc); !got.Equal(time.Date(2026, 12, 28, 0, 0, 0, 0, loc)) {
		t.Errorf("sept current start = %v, want 2026-12-28", got)
	}

	future3 := time.Date(2027, 1, 30, 0, 0, 0, 0, loc)
	fStart3, _ := billingBounds(future3, 0, loc)
	if got := time.Unix(fStart3, 0).In(loc); !got.Equal(time.Date(2027, 1, 27, 0, 0, 0, 0, loc)) {
		t.Errorf("sept current start = %v, want 2027-01-27", got)
	}
}

func TestPeriodBoundsInvariants(t *testing.T) {
	loc := time.Now().Location()
	billingDay = 29

	for _, period := range []string{"day", "week", "month"} {
		for _, offset := range []int{0, 1} {
			start, end := periodBounds(period, offset)
			if start >= end {
				t.Fatalf("%s offset=%d: start >= end", period, offset)
			}

			dur := time.Duration(end-start) * time.Second
			switch period {
			case "day":
				if offset == 1 && dur != 24*time.Hour {
					t.Errorf("day offset=1: dur = %v, want 24h", dur)
				}
				if offset == 0 && (dur <= 0 || dur > 24*time.Hour+time.Second) {
					t.Errorf("day offset=0: dur = %v, want (0, 24h]", dur)
				}
			case "week":
				if offset == 1 && dur != 7*24*time.Hour {
					t.Errorf("week offset=1: dur = %v, want 168h", dur)
				}
				if offset == 0 && (dur <= 0 || dur > 7*24*time.Hour+time.Second) {
					t.Errorf("week offset=0: dur = %v, want (0, 168h]", dur)
				}
			case "month":
				if offset == 1 && dur != 30*24*time.Hour {
					t.Errorf("month offset=1: dur = %v, want 720h", dur)
				}
				if offset == 0 && (dur <= 0 || dur > 30*24*time.Hour+time.Second) {
					t.Errorf("month offset=%d: dur = %v, want (0, 720h]", offset, dur)
				}
			}

			st := time.Unix(start, 0).In(loc)
			if st.Hour() != 0 || st.Minute() != 0 || st.Second() != 0 {
				t.Errorf("%s offset=%d: start not midnight: %v", period, offset, st)
			}
		}
	}
}

func TestBillingBoundsExactBoundaries(t *testing.T) {
	loc := time.UTC
	billingStart0 = time.Date(2026, 7, 30, 0, 0, 0, 0, loc)
	billingEnd0 = time.Date(2026, 8, 29, 0, 0, 0, 0, loc)
	t.Cleanup(func() { billingStart0 = time.Time{}; billingEnd0 = time.Time{} })

	// последняя секунда текущего периода
	now := time.Date(2026, 8, 29, 23, 59, 59, 0, loc)
	start, _ := billingBounds(now, 0, loc)
	if time.Unix(start, 0).In(loc) != billingStart0 {
		t.Errorf("8/29 23:59:59 должен относиться к текущему периоду")
	}

	// первая секунда следующего
	now = time.Date(2026, 8, 30, 0, 0, 0, 0, loc)
	start, _ = billingBounds(now, 0, loc)
	if got := time.Unix(start, 0).In(loc); got != time.Date(2026, 8, 30, 0, 0, 0, 0, loc) {
		t.Errorf("8/30 00:00:00 start = %v, want 2026-08-30", got)
	}

	// ровно +30 дней от начала следующего — уже третий период
	now = time.Date(2026, 9, 29, 0, 0, 0, 0, loc)
	start, _ = billingBounds(now, 0, loc)
	if got := time.Unix(start, 0).In(loc); got != time.Date(2026, 9, 29, 0, 0, 0, 0, loc) {
		t.Errorf("9/29 00:00:00 start = %v, want 2026-09-29", got)
	}
}

func TestCurrentBillingIndexSteps(t *testing.T) {
	loc := time.UTC
	billingEnd0 = time.Date(2026, 8, 29, 0, 0, 0, 0, loc)
	t.Cleanup(func() { billingEnd0 = time.Time{} })

	cases := []struct {
		now  time.Time
		want int
	}{
		{time.Date(2026, 8, 29, 23, 59, 0, 0, loc), 0},
		{time.Date(2026, 8, 30, 0, 0, 0, 0, loc), 1},
		{time.Date(2026, 9, 28, 12, 0, 0, 0, loc), 1},
		{time.Date(2026, 9, 29, 0, 0, 0, 0, loc), 2},
		{time.Date(2026, 10, 29, 0, 0, 0, 0, loc), 3},
	}
	for _, c := range cases {
		if got := currentBillingIndex(c.now, loc); got != c.want {
			t.Errorf("index(%v) = %d, want %d", c.now, got, c.want)
		}
	}
}

func TestBillingBoundsWithoutExplicitStart(t *testing.T) {
	loc := time.UTC
	billingStart0 = time.Time{}
	billingEnd0 = time.Date(2026, 8, 29, 0, 0, 0, 0, loc)
	t.Cleanup(func() { billingEnd0 = time.Time{} })

	now := time.Date(2026, 8, 24, 12, 0, 0, 0, loc)
	start, _ := billingBounds(now, 0, loc)
	want := time.Date(2026, 7, 31, 0, 0, 0, 0, loc)
	if got := time.Unix(start, 0).In(loc); got != want {
		t.Errorf("start без BILLING_START = %v, want %v", got, want)
	}
}

func TestBillingBoundsOffsetsChain(t *testing.T) {
	loc := time.Now().Location()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	billingStart0 = today.AddDate(0, 0, -25)
	billingEnd0 = today.AddDate(0, 0, 5)
	t.Cleanup(func() { billingStart0 = time.Time{}; billingEnd0 = time.Time{} })

	for o := 1; o <= 3; o++ {
		prevStart, _ := billingBounds(now, o-1, loc)
		_, curEnd := billingBounds(now, o, loc)
		if curEnd != prevStart {
			t.Errorf("offset %d: end (%d) != start предыдущего (%d)", o, curEnd, prevStart)
		}
	}

	_, end0 := billingBounds(now, 0, loc)
	if end0 != now.Unix()+1 {
		t.Errorf("offset 0: end = %d, want now", end0)
	}
}

func TestBillingStartAtNegative(t *testing.T) {
	loc := time.UTC
	billingEnd0 = time.Date(2026, 8, 29, 0, 0, 0, 0, loc)
	t.Cleanup(func() { billingEnd0 = time.Time{} })

	want := billingEnd0.AddDate(0, 0, -59) // 30*(-2)+1
	if got := billingStartAt(-1, loc); !got.Equal(want) {
		t.Errorf("startAt(-1) = %v, want %v", got, want)
	}
}

func TestPeriodBoundsWeekStartsMonday(t *testing.T) {
	loc := time.Now().Location()

	for _, offset := range []int{0, 1} {
		start, _ := periodBounds("week", offset)
		if wd := time.Unix(start, 0).In(loc).Weekday(); wd != time.Monday {
			t.Errorf("week offset=%d starts on %v, want Monday", offset, wd)
		}
	}

	s0, _ := periodBounds("week", 0)
	_, e1 := periodBounds("week", 1)
	if e1 != s0 {
		t.Errorf("prev week end (%d) != current week start (%d)", e1, s0)
	}
}

func TestPeriodBoundsMonthBillingDay(t *testing.T) {
	loc := time.Now().Location()
	billingDay = 29

	start, _ := periodBounds("month", 0)
	st := time.Unix(start, 0).In(loc)

	wantDay := 29
	if d := daysInMonth(st.Year(), st.Month()); d < wantDay {
		wantDay = d
	}
	if st.Day() != wantDay {
		t.Errorf("month start day = %d, want %d", st.Day(), wantDay)
	}

	s0, _ := periodBounds("month", 0)
	_, e1 := periodBounds("month", 1)
	if e1 != s0 {
		t.Errorf("prev month end (%d) != current month start (%d)", e1, s0)
	}
}

func TestPeriodBoundsRollingPriority(t *testing.T) {
	loc := time.Now().Location()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)

	billingDay = 1 // заведомо другое число
	billingStart0 = today.AddDate(0, 0, -25)
	billingEnd0 = today.AddDate(0, 0, 5)
	t.Cleanup(func() {
		billingStart0 = time.Time{}
		billingEnd0 = time.Time{}
		billingDay = 29
	})

	start, _ := periodBounds("month", 0)
	if start != billingStart0.Unix() {
		t.Errorf("periodBounds должен использовать rolling-даты, а не BILLING_DAY")
	}
}

func TestPeriodBoundsUnknownPeriod(t *testing.T) {
	start, end := periodBounds("year", 0)
	if start != 0 || end != 0 {
		t.Errorf("unknown period = (%d, %d), want (0, 0)", start, end)
	}
}

func TestPeriodBoundsLegacyClamp31(t *testing.T) {
	loc := time.Now().Location()
	billingStart0 = time.Time{}
	billingEnd0 = time.Time{}
	billingDay = 31
	t.Cleanup(func() { billingDay = 29 })

	start, _ := periodBounds("month", 0)
	st := time.Unix(start, 0).In(loc)

	wantDay := 31
	if d := daysInMonth(st.Year(), st.Month()); d < wantDay {
		wantDay = d
	}
	if st.Day() != wantDay {
		t.Errorf("legacy start day = %d, want %d", st.Day(), wantDay)
	}
}

func TestEnvInt(t *testing.T) {
	t.Setenv("TEST_INT", "42")
	if got := envInt("TEST_INT", 7); got != 42 {
		t.Errorf("envInt valid = %d, want 42", got)
	}

	t.Setenv("TEST_INT", "abc")
	if got := envInt("TEST_INT", 7); got != 7 {
		t.Errorf("envInt invalid = %d, want default 7", got)
	}

	if got := envInt("TEST_INT_MISSING", 7); got != 7 {
		t.Errorf("envInt missing = %d, want default 7", got)
	}
}

func TestEnvDurationSec(t *testing.T) {
	t.Setenv("TEST_DUR", "90")
	if got := envDurationSec("TEST_DUR", 60); got != 90*time.Second {
		t.Errorf("envDurationSec valid = %v, want 90s", got)
	}

	t.Setenv("TEST_DUR", "xyz")
	if got := envDurationSec("TEST_DUR", 60); got != 60*time.Second {
		t.Errorf("envDurationSec invalid = %v, want 60s", got)
	}
}

func TestEnvHelpersRejectNonPositive(t *testing.T) {
	t.Setenv("TEST_INT", "0")
	if got := envInt("TEST_INT", 7); got != 7 {
		t.Errorf("envInt(0) = %d, want default 7", got)
	}
	t.Setenv("TEST_INT", "-5")
	if got := envInt("TEST_INT", 7); got != 7 {
		t.Errorf("envInt(-5) = %d, want default 7", got)
	}

	t.Setenv("TEST_DUR", "0")
	if got := envDurationSec("TEST_DUR", 60); got != 60*time.Second {
		t.Errorf("envDurationSec(0) = %v, want 60s (иначе NewTicker паникует)", got)
	}
	t.Setenv("TEST_DUR", "-10")
	if got := envDurationSec("TEST_DUR", 60); got != 60*time.Second {
		t.Errorf("envDurationSec(-10) = %v, want 60s", got)
	}
}

func TestIsAdmin(t *testing.T) {
	AdminID := 123456

	ok, err := isAdmin(int64(AdminID), 123456)
	if err != nil || !ok {
		t.Errorf("isAdmin(123456) = (%v, %v), want (true, nil)", ok, err)
	}

	ok, err = isAdmin(int64(AdminID), 999)
	if err != nil || ok {
		t.Errorf("isAdmin(999) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestIsAdminNegativeChatID(t *testing.T) {
	ok, err := isAdmin(123456, -1001234567890)
	if err != nil || ok {
		t.Errorf("isAdmin(group chat) = (%v, %v), want (false, nil)", ok, err)
	}
}

func TestPeriodMap(t *testing.T) {
	expected := map[string]struct {
		code   string
		offset int
	}{
		"today":     {"day", 0},
		"week":      {"week", 0},
		"month":     {"month", 0},
		"yesterday": {"day", 1},
		"prevweek":  {"week", 1},
		"prevmonth": {"month", 1},
	}

	for key, want := range expected {
		p, ok := periodMap[key]
		if !ok {
			t.Fatalf("periodMap missing key %q", key)
		}
		if p.code != want.code || p.offset != want.offset {
			t.Errorf("periodMap[%q] = (%s, %d), want (%s, %d)", key, p.code, p.offset, want.code, want.offset)
		}
	}
}
