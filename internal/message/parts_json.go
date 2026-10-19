package message

// MarshalParts and UnmarshalParts expose the stored JSON form of a message's
// parts, [{"type": …, "data": {…}}], for tools that read and edit messages
// outside the agent loop, such as `crush session web`.
func MarshalParts(parts []ContentPart) ([]byte, error) { return marshalParts(parts) }

// UnmarshalParts is the inverse of MarshalParts.
func UnmarshalParts(data []byte) ([]ContentPart, error) { return unmarshalParts(data) }
