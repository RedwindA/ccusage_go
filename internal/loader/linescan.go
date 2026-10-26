package loader

import (
	"bytes"
	"slices"

	"github.com/RedwindA/ccusage_go/internal/jsonscan"
)

// Transcript lines are dominated by prompt text, thinking and tool output
// that the loader never reads. scanLine validates each line in one pass but
// materializes only the fields below, so decoding cost tracks the handful of
// metadata and usage fields instead of the line size.

// lineTopKeys are the top-level fields read by loadFileWithDedupe,
// parseEntry, validateUsageData, advisorRecords, agentProgressRecord,
// shouldCountAsParseError and rawRetainKeys. Every other key is skipped;
// clearRawExceptKeys would drop it before an entry leaves the loader anyway.
var lineTopKeys = map[string]bool{
	"agentName": true, "aiTitle": true, "block_type": true, "cache_creation_input_tokens": true,
	"cache_read_input_tokens": true, "cost": true, "costUSD": true, "customTitle": true, "cwd": true,
	"data": true, "id": true, "isMeta": true, "isSidechain": true, "message": true, "model": true,
	"project_path": true, "requestId": true, "sessionId": true, "timestamp": true, "type": true, "version": true,
	// rawRetainKeys
	"usage_limit_reset_time": true, "cache_creation": true, "service_tier": true, "message_id": true, "request_id": true,
}

// lineMessageKeys are the message fields the loader reads; content, the bulk
// of every assistant line, is skipped.
var lineMessageKeys = map[string]bool{"id": true, "usage": true, "model": true, "diagnostics": true}

// lineDataKeys and lineProgressKeys are the fields of data and of the
// assistant record an AgentProgress line wraps under data.message that
// agentProgressRecord reads; that record's message keeps lineMessageKeys.
var (
	lineDataKeys     = map[string]bool{"message": true}
	lineProgressKeys = map[string]bool{"costUSD": true, "isSidechain": true, "message": true, "requestId": true, "timestamp": true}
)

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
		switch {
		case string(key) == "message" && value.Kind() == '{':
			v, err = scanMessage(value)
		case string(key) == "data" && value.Kind() == '{':
			v, err = scanObject(value, lineDataKeys, scanProgressRecord)
		default:
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
	return scanObject(value, lineMessageKeys, nil)
}

func scanProgressRecord(value jsonscan.Value) (map[string]interface{}, error) {
	return scanObject(value, lineProgressKeys, scanMessage)
}

// scanObject decodes the members of the object value that keys names,
// scanning an object-valued "message" member with message when it is set.
func scanObject(value jsonscan.Value, keys map[string]bool, message func(jsonscan.Value) (map[string]interface{}, error)) (map[string]interface{}, error) {
	object := map[string]interface{}{}
	_, err := value.Fields(func(key []byte, value jsonscan.Value) error {
		if !keys[string(key)] {
			return nil
		}
		var v interface{}
		var err error
		if message != nil && string(key) == "message" && value.Kind() == '{' {
			v, err = message(value)
		} else {
			v, err = value.Decode()
		}
		object[string(key)] = v
		return err
	})
	return object, err
}

// nonNullableKeys are the fields upstream skips a usage line for holding as
// null, at any depth of the line.
var nonNullableKeys = map[string]bool{
	"id": true, "cwd": true, "model": true, "speed": true, "costUSD": true, "version": true, "sessionId": true,
	"requestId": true, "isApiErrorMessage": true, "cache_read_input_tokens": true, "cache_creation_input_tokens": true,
}

// iterationPaths are where upstream lets an element of usage.iterations
// hold a null model, on an assistant line and on an AgentProgress line.
var iterationPaths = [][]string{{"message", "usage", "iterations"}, {"data", "message", "message", "usage", "iterations"}}

// hasNullField reports whether line, a JSON object, holds one of
// nonNullableKeys as null, as upstream's has_unsupported_null_field_in_value
// rejects it. Only lines with such a key before a null are decoded whole.
func hasNullField(line []byte) bool {
	for rest := line; ; {
		i := bytes.Index(rest, []byte("null"))
		if i < 0 {
			return false
		}
		before := bytes.TrimRight(rest[:i], " \t\r\n")
		rest = rest[i+4:]
		if before, ok := bytes.CutSuffix(before, []byte(":")); ok {
			before = bytes.TrimRight(before, " \t\r\n")
			if before, ok := bytes.CutSuffix(before, []byte(`"`)); ok {
				if key := before[bytes.LastIndexByte(before, '"')+1:]; nonNullableKeys[string(key)] {
					break
				}
			}
		}
	}
	root, err := jsonscan.Decode(line)
	return err == nil && nullField(root, nil, false)
}

// nullField walks v, found at path, for hasNullField. An array element adds
// "" to the path; the elements of the arrays at iterationPaths may hold a
// null model.
func nullField(v interface{}, path []string, nullModel bool) bool {
	switch v := v.(type) {
	case map[string]interface{}:
		for key, value := range v {
			if value == nil && nonNullableKeys[key] && !(nullModel && key == "model") {
				return true
			}
			if nullField(value, append(path[:len(path):len(path)], key), false) {
				return true
			}
		}
	case []interface{}:
		iterations := slices.ContainsFunc(iterationPaths, func(p []string) bool { return slices.Equal(p, path) })
		for _, element := range v {
			if nullField(element, append(path[:len(path):len(path)], ""), iterations) {
				return true
			}
		}
	}
	return false
}
