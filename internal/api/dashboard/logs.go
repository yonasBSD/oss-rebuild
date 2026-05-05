// Copyright 2025 Google LLC
// SPDX-License-Identifier: Apache-2.0

package dashboard

import (
	"bufio"
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"

	"cloud.google.com/go/storage"
	"github.com/google/oss-rebuild/pkg/gcb"
	"github.com/google/oss-rebuild/pkg/rebuild/rebuild"
)

type LogsRequest struct {
	Ecosystem string
	Package   string
	Version   string
	Artifact  string
	RunID     string
}

func (LogsRequest) Validate() error { return nil }

func fetchLogObject(ctx context.Context, req LogsRequest, deps *Deps) (*storage.ObjectHandle, *storage.ObjectAttrs, error) {
	attempt, err := deps.Rundex.FetchAttempt(ctx, rebuild.Target{
		Ecosystem: rebuild.Ecosystem(req.Ecosystem),
		Package:   req.Package,
		Version:   req.Version,
		Artifact:  req.Artifact,
	}, req.RunID)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching attempt: %w", err)
	}

	logID := attempt.BuildID
	if logID == "" {
		logID = attempt.ObliviousID
	}
	if logID == "" {
		return nil, nil, fmt.Errorf("no logs available for this attempt")
	}

	obj := deps.GCSClient.Bucket(deps.LogsBucket).Object(gcb.MergedLogFile(logID))
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching log attributes: %w", err)
	}

	return obj, attrs, nil
}

func HandleRawLogs(w http.ResponseWriter, r *http.Request, req LogsRequest, deps *Deps) {
	if deps.GCSClient == nil || deps.LogsBucket == "" {
		http.Error(w, "Log viewing is not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	obj, _, err := fetchLogObject(ctx, req, deps)
	if err != nil {
		log.Printf("Error fetching logs: %v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	reader, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Error creating log reader: %v", err)
		http.Error(w, "Failed to create log reader", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("Error streaming logs: %v", err)
	}
}

func HandleLogs(w http.ResponseWriter, r *http.Request, req LogsRequest, deps *Deps) {
	if deps.GCSClient == nil || deps.LogsBucket == "" {
		http.Error(w, "Log viewing is not configured", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	obj, attrs, err := fetchLogObject(ctx, req, deps)
	if err != nil {
		log.Printf("Error fetching logs: %v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if attrs.Size > 10*1024*1024 {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
    <style>
        body { background: #121212; color: #e0e0e0; font-family: monospace; margin: 0; padding: 20px; display: flex; flex-direction: column; align-items: center; justify-content: center; height: 100vh; }
        .warning-box { background: #333; padding: 30px; border-radius: 8px; border: 1px solid #555; text-align: center; max-width: 600px; }
        a { color: #4fc3f7; text-decoration: none; font-weight: bold; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <div class="warning-box">
        <h2>Logs too large to display directly</h2>
        <p>The logs for this build exceed the display limit (10MB). Rendering them in the browser might cause performance issues.</p>
        <p><a href="raw/">Click here to view the raw logs</a></p>
    </div>
</body>
</html>`)
		return
	}

	reader, err := obj.NewReader(ctx)
	if err != nil {
		log.Printf("Error creating log reader: %v", err)
		http.Error(w, "Failed to create log reader", http.StatusInternalServerError)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head>
    <style>
        body { background: #121212; color: #e0e0e0; font-family: monospace; margin: 0; padding: 10px; }
        .log-line { display: flex; line-height: 1.2; }
        .line-number { color: #888; text-decoration: none; padding-right: 15px; user-select: none; text-align: right; min-width: 40px; cursor: pointer; }
        .line-content { word-break: break-all; white-space: pre-wrap; }
        .highlight { background-color: #444; color: #fff; }
    </style>
</head>
<body>
`)

	scanner := bufio.NewScanner(reader)
	lineNumber := 1
	for scanner.Scan() {
		line := scanner.Text()
		escapedLine := html.EscapeString(line)
		fmt.Fprintf(w, `<div class="log-line" id="L%d"><a class="line-number">%d</a><span class="line-content">%s</span></div>`, lineNumber, lineNumber, escapedLine)
		lineNumber++
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error streaming logs: %v", err)
	}

	fmt.Fprint(w, `
<script>
    var lastClicked = null;

    function highlightLines(scrollIntoView) {
        document.querySelectorAll('.highlight').forEach(el => el.classList.remove('highlight'));
        var hash = window.location.hash;
        if (!hash) {
            if (scrollIntoView) window.scrollTo(0, document.body.scrollHeight);
            return;
        }
        var match = hash.match(/^#L(\d+)(?:-L(\d+))?$/);
        if (match) {
            var start = parseInt(match[1], 10);
            var end = match[2] ? parseInt(match[2], 10) : start;
            if (start > end) { var temp = start; start = end; end = temp; }
            for (var i = start; i <= end; i++) {
                var el = document.getElementById('L' + i);
                if (el) el.classList.add('highlight');
            }
            if (scrollIntoView) {
                var startEl = document.getElementById('L' + start);
                if (startEl) startEl.scrollIntoView();
            }
        }
    }

    function updateHash(newHash) {
        if (window.location.hash !== newHash) {
            history.replaceState(null, null, newHash);
            highlightLines(false);
        }
    }

    document.addEventListener('click', function(e) {
        var lineNum = findLineNum(e.target);
        if (lineNum) {
            var selection = window.getSelection();
            if (!selection.isCollapsed) return; // If we're selecting text, don't trigger click highlight
            
            var newHash;
            if (e.shiftKey && lastClicked !== null) {
                newHash = '#L' + lastClicked + '-L' + lineNum;
            } else {
                newHash = '#L' + lineNum;
                lastClicked = lineNum;
            }
            updateHash(newHash);
        }
    });

    function findLineNum(node) {
        while (node) {
            if (node.id && node.id.startsWith('L')) return parseInt(node.id.substring(1), 10);
            node = node.parentElement;
        }
        return null;
    }

    document.addEventListener('selectionchange', function() {
        var selection = window.getSelection();
        if (selection.isCollapsed) return;

        var range = selection.getRangeAt(0);
        var startLine = findLineNum(range.startContainer);
        var endLine = findLineNum(range.endContainer);

        if (startLine && endLine) {
            var newHash = (startLine === endLine) ? '#L' + startLine : '#L' + startLine + '-L' + endLine;
            updateHash(newHash);
        }
    });

    window.addEventListener('hashchange', function() { highlightLines(true); });
    
    // Initial highlight
    if (window.location.hash) {
        highlightLines(true);
    } else {
        highlightLines(true); // Scrolls to bottom
    }
</script>
</body>
</html>`)
}
