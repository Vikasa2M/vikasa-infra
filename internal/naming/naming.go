// Package naming owns the NATS naming and subject-space conventions shared by
// planning, account modelling, and credential issuance. Every tool that decides
// whether a subject is inside a district's boundary — or resolves that boundary
// — must use these helpers, so the generator and the issuer can never disagree.
package naming

import "strings"

// UnderPrefix reports whether subject lies within the subject space named by
// prefix. A well-formed space ends in ".>" (e.g. "vikasa.exdot.d7.>"); the
// match is on a token boundary, so "vikasa.exdot.d70.x" is NOT under
// "vikasa.exdot.d7.>". Mirroring NATS matching, the bare space root
// ("vikasa.exdot.d7") is not matched by ".>" either. A prefix without the
// ".>" convention never widens into a raw string-prefix match: it matches
// only the exact subject.
func UnderPrefix(subject, prefix string) bool {
	if base, ok := strings.CutSuffix(prefix, ".>"); ok {
		return strings.HasPrefix(subject, base+".")
	}
	return subject == prefix
}

// ValidSubjectString reports whether s is a well-formed NATS subject safe to
// emit into generated config: one or more '.'-separated tokens, each a non-empty
// run of [A-Za-z0-9_-], or a single wildcard token ('*', or '>' as the final
// token). It deliberately rejects the quoting, brace, whitespace and control
// characters that would otherwise let a spec-supplied subject break out of its
// quoted position in accounts.conf (config injection). Callers in
// topology.Validate gate subject-prefix / from / as through this before those
// strings can reach the plan and render layers.
func ValidSubjectString(s string) bool {
	if s == "" {
		return false
	}
	tokens := strings.Split(s, ".")
	for i, tok := range tokens {
		switch tok {
		case "":
			return false // empty token: leading/trailing/double dot
		case "*":
			continue
		case ">":
			if i != len(tokens)-1 {
				return false // '>' is only valid as the final token
			}
			continue
		}
		for _, r := range tok {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
				r >= '0' && r <= '9' || r == '_' || r == '-'
			if !ok {
				return false
			}
		}
	}
	return true
}

// Sanitize uppercases s and replaces every '/' and '-' with '_' — the shared
// convention for deriving NATS stream/account/operator names from spec ids.
// It is deliberately NOT injective ('/' and '-' collide); plan.Build rejects
// topologies whose stream names collide after sanitization.
func Sanitize(s string) string {
	s = strings.ToUpper(s)
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

// DistrictAccountName is the NATS account name for a district. Issuance signs
// cabinet JWTs against this exact name, so it must match the accounts model.
func DistrictAccountName(districtID string) string {
	return "DISTRICT_" + Sanitize(districtID)
}

// OperatorName is the NATS operator name for a DOT.
func OperatorName(dot string) string {
	return "VIKASA_" + Sanitize(dot)
}

// PartitionStreamName returns the NATS stream name for a partition:
// VIKASA_<DOT>_<DISTRICT>_<PART>, sanitized per Sanitize.
func PartitionStreamName(dot, district, partID string) string {
	return "VIKASA_" + Sanitize(dot) + "_" + Sanitize(district) + "_" + Sanitize(partID)
}

// CentralStreamName returns the NATS stream name for the central stream.
func CentralStreamName(dot string) string {
	return "VIKASA_" + Sanitize(dot) + "_CENTRAL"
}

// CentralShardStreamName returns the NATS stream name for a per-partition central
// aggregation shard: VIKASA_<DOT>_CENTRAL_<DISTRICT>_<PART>. Central is sharded
// per partition (finding C1) so no single leader holds the whole DOT.
func CentralShardStreamName(dot, district, partID string) string {
	return "VIKASA_" + Sanitize(dot) + "_CENTRAL_" + Sanitize(district) + "_" + Sanitize(partID)
}

// DMZStreamName returns the NATS stream name for the DMZ egress stream.
func DMZStreamName(dot string) string {
	return "VIKASA_" + Sanitize(dot) + "_DMZ"
}

// BufferStreamName is the fixed stream name of a cabinet's local buffer stream.
// Regional partition streams source it per cabinet; render/diagram.go filters
// sources on it. Both sides must agree, so it lives here rather than inline.
func BufferStreamName() string { return "VIKASA_BUFFER" }

// RootSpace is the whole-project subject space "vikasa.>" — every DOT's
// anchor sits under it. Used as the DMZ stream's core-NATS republish
// source/dest (a loop-safe fan-out echo on a source-only stream).
func RootSpace() string { return "vikasa.>" }

// DefaultSubjectPrefix is a district's subject boundary when none is declared.
// The only fixed anchor is vikasa.<dot>.; everything below is the DOT's.
func DefaultSubjectPrefix(dot, districtID string) string {
	return "vikasa." + dot + "." + districtID + ".>"
}

// SubjectSpace resolves a district's subject boundary: the declared prefix
// when present, else the default vikasa.<dot>.<district>.> space.
func SubjectSpace(dot, districtID string, declared *string) string {
	if declared != nil {
		return *declared
	}
	return DefaultSubjectPrefix(dot, districtID)
}

// Anchor is the fixed DOT subject anchor "vikasa.<dot>." — the only prefix
// every district subject space starts with.
func Anchor(dot string) string { return "vikasa." + dot + "." }

// ShareSpace is the DMZ public share space prefix "vikasa.<dot>.share." — the
// deny-by-default egress boundary. A well-formed share space is ShareSpace(dot)+"<name>.>".
func ShareSpace(dot string) string { return "vikasa." + dot + ".share." }

// PeerSpace is the quarantined inbound peer-DOT space prefix "vikasa.peer.<dot>.".
func PeerSpace(dot string) string { return "vikasa.peer." + dot + "." }

// LeafDNSName is the DNS record for a partition's leaf endpoint:
// leaf-<dot>-<partDNS>.nats.vikasa.<dot>, where partDNS is partitionID
// lowercased with '/' replaced by '-'. Partitions whose leaf-DNS segments
// collide are rejected by plan.Build.
func LeafDNSName(dot, partitionID string) string {
	partDNS := strings.ReplaceAll(strings.ToLower(partitionID), "/", "-")
	return "leaf-" + strings.ToLower(dot) + "-" + partDNS + ".nats.vikasa." + strings.ToLower(dot)
}

// CentralAccountName, DMZAccountName, SystemAccountName are the fixed NATS
// account names (not derived from spec ids, so no Sanitize).
func CentralAccountName() string { return "CENTRAL" }
func DMZAccountName() string     { return "DMZ" }
func SystemAccountName() string  { return "SYSTEM" }

// FilterUnderDistrict resolves a district's subject space (declared or default)
// and reports whether filter lies within it. Callers format their own errors so
// their existing (intentionally distinct) wording is preserved.
func FilterUnderDistrict(dot, districtID string, declared *string, filter string) (space string, ok bool) {
	space = SubjectSpace(dot, districtID, declared)
	return space, UnderPrefix(filter, space)
}
