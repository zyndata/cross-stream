package dns

import "testing"

func FuzzParse(f *testing.F) {
	f.Add(buildQuery(1, "example.com", 1232))
	f.Add(buildAnswer(buildQuery(2, "a.b", 0), rcodeOK, 60, 3, 0))
	f.Fuzz(func(t *testing.T, b []byte) {
		if q, err := parseQuery(b); err == nil {
			_ = servfail(b, q.qEnd)
		}
		if offs, _, err := answerTTLs(b); err == nil {
			for _, o := range offs {
				if o+4 > len(b) {
					t.Fatalf("ttl offset %d past %d", o, len(b))
				}
			}
		}
	})
}
