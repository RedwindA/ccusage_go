package sourcesa

import (
	"time"

	"github.com/RedwindA/ccusage_go/internal/types"
)

type codexSnapshotKey struct {
	at    time.Time
	model string
	usage codexUsage
}

// mergeCodexCopies settles attribution before any report filters or groups
// usage. Each rollout can record several real requests with the same counts
// and timestamp. Only copies across rollouts collapse.
func mergeCodexCopies(entries []types.UsageEntry, parents map[string]string, replayed map[int]bool) []types.UsageEntry {
	first := make(map[codexSnapshotKey]int, len(entries))
	copies := map[int][]int{}
	compactions := map[string]int{}
	dropped := make([]bool, len(entries))
	for i := range replayed {
		dropped[i] = true
	}
	merge := func(keep, drop int) {
		if codexTierRank(entries[drop].Speed) > codexTierRank(entries[keep].Speed) {
			entries[keep].Speed = entries[drop].Speed
		}
		dropped[drop] = true
	}
	for i := range entries {
		e := &entries[i]
		if id, _ := e.Raw["response_id"].(string); id != "" {
			if replayed[i] {
				continue
			}
			if keep, ok := compactions[id]; ok {
				merge(keep, i)
			} else {
				compactions[id] = i
			}
			continue
		}
		key := codexSnapshotKey{e.Timestamp.UTC(), e.Model, codexUsage{
			e.InputTokens + e.CacheReadInputTokens + e.CacheCreationInputTokens,
			e.CacheReadInputTokens, e.CacheCreationInputTokens, e.OutputTokens, e.ReasoningTokens, e.TotalTokens,
		}}
		if keep, ok := first[key]; ok {
			if copies[keep] == nil {
				copies[keep] = []int{keep}
			}
			copies[keep] = append(copies[keep], i)
		} else {
			first[key] = i
		}
	}
	for _, group := range copies {
		byFile := map[string]int{}
		var files [][]int
		recorded := map[string]int{}
		for _, i := range group {
			file := entries[i].SourceFile
			n, ok := byFile[file]
			if !ok {
				n = len(files)
				byFile[file] = n
				files = append(files, nil)
			}
			files[n] = append(files[n], i)
			if id := codexRecordID(&entries[i]); id != "" && !replayed[i] {
				if keep, ok := recorded[id]; ok {
					merge(keep, i)
				} else {
					recorded[id] = i
				}
			}
		}
		for _, slot := range alignCodexOccurrences(entries, files) {
			matches := slot.entries
			if len(matches) == 1 {
				continue
			}
			threads := map[string]bool{}
			for _, i := range matches {
				threads[codexThread(&entries[i])] = true
			}
			// One ordinal covers both recorded and legacy snapshots.
			// Requests on different fork depths are independent. A known
			// record or replay displaces legacy copies at its own depth.
			byDepth := map[int][]int{}
			for _, i := range matches {
				depth := codexMatchDepth(codexThread(&entries[i]), parents, threads)
				byDepth[depth] = append(byDepth[depth], i)
			}
			for _, sameDepth := range byDepth {
				var replayIDs, replayThreads map[string]bool
				for _, i := range sameDepth {
					if !replayed[i] {
						continue
					}
					if replayThreads == nil {
						replayThreads = map[string]bool{}
						replayIDs = map[string]bool{}
					}
					replayThreads[codexRecordedThread(&entries[i])] = true
					if id := codexRecordID(&entries[i]); id != "" {
						replayIDs[id] = true
					}
				}
				var legacy []int
				var named []int
				for _, i := range sameDepth {
					if replayed[i] {
						continue
					}
					id := codexRecordID(&entries[i])
					thread := codexRecordedThread(&entries[i])
					switch {
					case replayIDs[id], id == "" && (replayThreads[thread] || thread == "" && len(replayThreads) > 0):
						dropped[i] = true
					case id != "":
						named = append(named, i)
					default:
						legacy = append(legacy, i)
					}
				}
				if len(named) == 0 {
					for n := 1; n < len(legacy); n++ {
						merge(legacy[0], legacy[n])
					}
					continue
				}
				independent := map[string]int{}
				for _, i := range legacy {
					thread := codexRecordedThread(&entries[i])
					parent := parents[thread]
					reference := entries[i].Raw["reference_fork"] == true
					match, ambiguous, sibling := "", false, false
					for _, j := range named {
						other := codexRecordedThread(&entries[j])
						if thread != "" && other != "" && thread != other {
							// Reference siblings have no physical history
							// to replay. Keep their independent work even
							// when only one rollout recorded a response ID.
							sibling = sibling || reference && entries[j].Raw["reference_fork"] == true &&
								parent != "" && parents[other] == parent
							continue
						}
						id := codexRecordID(&entries[j])
						ambiguous = ambiguous || match != "" && match != id
						match = id
					}
					switch {
					case match != "" && !ambiguous:
						merge(recorded[match], i)
					case match == "" && sibling:
						if keep, ok := independent[thread]; ok {
							merge(keep, i)
						} else {
							independent[thread] = i
						}
					default:
						dropped[i] = true
					}
				}
			}
		}
	}
	// A paired snapshot can already cover the compaction that another
	// rollout records as a standalone request.
	if len(compactions) > 0 {
		for i := range entries {
			if !dropped[i] {
				if standalone, ok := compactions[codexRecordID(&entries[i])]; ok {
					merge(i, standalone)
				}
			}
		}
	}
	kept := entries[:0]
	for i, e := range entries {
		if !dropped[i] {
			kept = append(kept, e)
		}
	}
	return kept
}

type codexOccurrence struct {
	entries []int
	pos     int
}

// alignCodexOccurrences matches each file in order. Shared response IDs
// partition the sequences; unmatched requests keep their own slots even
// when a partial rollout omits entries between two matching IDs.
func alignCodexOccurrences(entries []types.UsageEntry, files [][]int) []*codexOccurrence {
	var slots []*codexOccurrence
	positions := map[string]*codexOccurrence{}
	for _, file := range files {
		var anchors [][2]int
		nextSlot := 0
		for n, i := range file {
			if slot := positions[codexRecordID(&entries[i])]; slot != nil && slot.pos >= nextSlot {
				anchors = append(anchors, [2]int{slot.pos, n})
				nextSlot = slot.pos + 1
			}
		}
		aligned := make([]*codexOccurrence, 0, max(len(slots), len(file)))
		s, n := 0, 0
		segment := func(slotEnd, fileEnd int) {
			for s < slotEnd || n < fileEnd {
				var slot *codexOccurrence
				if s < slotEnd {
					slot = slots[s]
					s++
				} else {
					slot = &codexOccurrence{}
				}
				if n < fileEnd {
					i := file[n]
					slot.entries = append(slot.entries, i)
					if id := codexRecordID(&entries[i]); id != "" && positions[id] == nil {
						positions[id] = slot
					}
					n++
				}
				slot.pos = len(aligned)
				aligned = append(aligned, slot)
			}
		}
		for _, anchor := range anchors {
			segment(anchor[0], anchor[1])
			segment(anchor[0]+1, anchor[1]+1)
		}
		segment(len(slots), len(file))
		slots = aligned
	}
	return slots
}

func codexRecordID(e *types.UsageEntry) string {
	id, _ := e.Raw["record_response_id"].(string)
	return id
}

func codexThread(e *types.UsageEntry) string {
	if id := codexRecordedThread(e); id != "" {
		return id
	}
	return e.SessionID
}

func codexRecordedThread(e *types.UsageEntry) string {
	id, _ := e.Raw["session_uuid"].(string)
	return id
}

func codexMatchDepth(thread string, parents map[string]string, matches map[string]bool) int {
	seen := map[string]bool{thread: true}
	depth := 0
	for parent := parents[thread]; parent != "" && !seen[parent]; parent = parents[parent] {
		seen[parent] = true
		if matches[parent] {
			depth++
		}
	}
	return depth
}

func codexTierRank(speed string) int {
	switch speed {
	case "standard":
		return 4
	case "ultrafast":
		return 3
	case "fast":
		return 2
	case "flex":
		return 1
	}
	return 0
}
