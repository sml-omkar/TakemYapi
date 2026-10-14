package freshservice

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// imgTagPattern finds <img ...> tags (case-insensitive, spanning attributes).
var imgTagPattern = regexp.MustCompile(`(?is)<img\b[^>]*>`)

// srcPattern extracts the src attribute from a single <img> tag.
var srcPattern = regexp.MustCompile(`(?i)\bsrc\s*=\s*["']([^"']+)["']`)

// dataIDPattern extracts the data-id attribute (the inline attachment ID).
var dataIDPattern = regexp.MustCompile(`(?i)\bdata-id\s*=\s*["']?(\d+)["']?`)

// altPattern extracts the alt attribute (used as the file name hint).
var altPattern = regexp.MustCompile(`(?i)\balt\s*=\s*["']([^"']*)["']`)

// ExtractInlineAttachments scans HTML (ticket description or conversation
// body) for embedded <img src=".../inline/attachment?token=..."> tags and
// returns one InlineAttachment per unique image. convID is 0 for the ticket
// description, otherwise the owning conversation ID.
func ExtractInlineAttachments(html string, convID int64) []InlineAttachment {
	if !strings.Contains(strings.ToLower(html), "<img") {
		return nil
	}
	var out []InlineAttachment
	seen := map[string]bool{}
	for _, tag := range imgTagPattern.FindAllString(html, -1) {
		srcMatch := srcPattern.FindStringSubmatch(tag)
		if len(srcMatch) != 2 {
			continue
		}
		src := strings.TrimSpace(srcMatch[1])
		if src == "" || !strings.Contains(src, "inline/attachment") {
			continue
		}
		var id int64
		if m := dataIDPattern.FindStringSubmatch(tag); len(m) == 2 {
			if parsed, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				id = parsed
			}
		}
		name := ""
		if m := altPattern.FindStringSubmatch(tag); len(m) == 2 {
			name = strings.TrimSpace(m[1])
		}
		if name == "" {
			if id != 0 {
				name = fmt.Sprintf("inline_%d", id)
			} else {
				name = "inline_image"
			}
		}
		// Preserve an image extension when the alt name lacks one; the
		// download is near-always a PNG screenshot.
		if !strings.Contains(name, ".") {
			name += ".png"
		}
		key := strconv.FormatInt(id, 10) + "|" + src
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, InlineAttachment{
			ID:             id,
			Name:           name,
			URL:            src,
			ConversationID: convID,
		})
	}
	return out
}

// CollectInlineAttachments extracts inline images from the ticket description
// and every conversation body, populating Ticket.InlineAttachments and each
// Conversation.InlineAttachments in place. It returns the total count found.
// Already-recorded DownloadedPaths (same conversation + ID + URL) are
// preserved, so re-running over a cached ticket never loses download state.
func CollectInlineAttachments(t *Ticket) int {
	keepTicket := indexInlinePaths(t.InlineAttachments)
	t.InlineAttachments = restoreInlinePaths(ExtractInlineAttachments(t.Description, 0), keepTicket)
	total := len(t.InlineAttachments)
	for i := range t.Conversations {
		c := &t.Conversations[i]
		keep := indexInlinePaths(c.InlineAttachments)
		c.InlineAttachments = restoreInlinePaths(ExtractInlineAttachments(c.Body, c.ID), keep)
		total += len(c.InlineAttachments)
	}
	return total
}

// indexInlinePaths snapshots downloaded paths keyed by attachment ID + URL.
func indexInlinePaths(ins []InlineAttachment) map[string]string {
	keep := map[string]string{}
	for _, in := range ins {
		if in.DownloadedPath != "" {
			keep[inlineKey(in.ID, in.URL)] = in.DownloadedPath
		}
	}
	return keep
}

// restoreInlinePaths copies back any previously recorded download paths.
func restoreInlinePaths(ins []InlineAttachment, keep map[string]string) []InlineAttachment {
	for i := range ins {
		if p, ok := keep[inlineKey(ins[i].ID, ins[i].URL)]; ok {
			ins[i].DownloadedPath = p
		}
	}
	return ins
}

func inlineKey(id int64, url string) string {
	return strconv.FormatInt(id, 10) + "|" + url
}
