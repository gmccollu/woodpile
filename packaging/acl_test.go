package packaging

import "testing"

func TestInheritedDACL(t *testing.T) {
	sid := WindowsServiceSID
	for _, tc := range []struct {
		dir  bool
		want string
	}{
		{true, "D:AI(A;OICIID;FA;;;SY)(A;OICIID;FA;;;BA)(A;OICIID;0x1301bf;;;" + sid + ")"},
		{false, "D:AI(A;ID;FA;;;SY)(A;ID;FA;;;BA)(A;ID;0x1301bf;;;" + sid + ")"},
	} {
		got, err := InheritedDACL(tc.dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("InheritedDACL(dir=%v) = %s, want %s", tc.dir, got, tc.want)
		}
	}
}
