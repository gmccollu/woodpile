// Package winacl gives data folders a protected ACL on Windows (security review
// M3). Elsewhere it does nothing: Unix folders get mode 0750 from the code that
// creates them.
package winacl
