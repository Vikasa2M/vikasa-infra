package plan

import (
	"github.com/Vikasa2M/vikasa-infra/internal/naming"
)

// PartitionStreamName returns the NATS stream name for a partition.
// Format: VIKASA_<DOT>_<DISTRICT>_<PART> (see naming.Sanitize).
func PartitionStreamName(dot, district, partID string) string {
	return naming.PartitionStreamName(dot, district, partID)
}

// CentralStreamName returns the NATS stream name for the central stream.
// Format: VIKASA_<DOT>_CENTRAL.
func CentralStreamName(dot string) string {
	return naming.CentralStreamName(dot)
}

// CentralShardStreamName returns the NATS stream name for a per-partition central
// aggregation shard. Format: VIKASA_<DOT>_CENTRAL_<DISTRICT>_<PART>.
func CentralShardStreamName(dot, district, partID string) string {
	return naming.CentralShardStreamName(dot, district, partID)
}

// SubjectSpace resolves a district's subject boundary (declared prefix or default).
func SubjectSpace(dot, districtID string, declared *string) string {
	return naming.SubjectSpace(dot, districtID, declared)
}

// UnderPrefix reports whether subject lies within the subject space named by prefix.
func UnderPrefix(subject, prefix string) bool {
	return naming.UnderPrefix(subject, prefix)
}

// DMZStreamName returns the NATS stream name for the DMZ egress stream.
// Format: VIKASA_<DOT>_DMZ.
func DMZStreamName(dot string) string {
	return naming.DMZStreamName(dot)
}
