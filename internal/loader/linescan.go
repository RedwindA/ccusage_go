package loader

import "github.com/RedwindA/ccusage_go/internal/jsonscan"

// Transcript lines are dominated by prompt text, thinking and tool output
// that the loader never reads. scanLine validates each line in one pass but
// materializes only the fields below, so decoding cost tracks the handful of
// metadata and usage fields instead of the line size.

// lineTopKeys are the top-level fields read by loadFileWithDedupe,
// parseEntry, validateUsageData, advisorRecords, shouldCountAsParseError and
// rawRetainKeys. Every other key is skipped; clearRawExceptKeys would drop
// it before an entry leaves the loader anyway.
var lineTopKeys = map[string]bool{
	"agentName": true, "aiTitle": true, "block_type": true, "cache_creation_input_tokens": true,
	"cache_read_input_tokens": true, "cost": true, "costUSD": true, "customTitle": true, "cwd": true,
	"id": true, "isMeta": true, "isSidechain": true, "message": true, "model": true, "project_path": true,
	"requestId": true, "session_id": true, "sessionId": true, "timestamp": true, "type": true,
	// rawRetainKeys
	"usage_limit_reset_time": true, "cache_creation": true, "service_tier": true, "message_id": true, "request_id": true,
}

// lineMessageKeys are the message fields the loader reads; content, the bulk
// of every assistant line, is skipped.
var lineMessageKeys = map[string]bool{"id": true, "usage": true, "model": true, "diagnostics": true}

// scanLine returns the loader-relevant subset of the JSON object in line,
// with the values json.Unmarshal into map[string]interface{} would produce
// for those keys. A top-level null yields a nil map; any syntax error rejects
// the whole line, as json.Unmarshal would.
func scanLine(line []byte) (map[string]interface{}, error) {
	raw := map[string]interface{}{}
	null, err := jsonscan.FieldsStrict(line, func(key []byte, value jsonscan.Value) error {
		if !lineTopKeys[string(key)] {
			return nil
		}
		var v interface{}
		var err error
		if string(key) == "message" && value.Kind() == '{' {
			v, err = scanMessage(value)
		} else {
			v, err = value.Decode()
		}
		raw[string(key)] = v
		return err
	})
	if err != nil || null {
		return nil, err
	}
	return raw, nil
}

func scanMessage(value jsonscan.Value) (map[string]interface{}, error) {
	message := map[string]interface{}{}
	_, err := value.Fields(func(key []byte, value jsonscan.Value) error {
		if !lineMessageKeys[string(key)] {
			return nil
		}
		v, err := value.Decode()
		message[string(key)] = v
		return err
	})
	return message, err
}
