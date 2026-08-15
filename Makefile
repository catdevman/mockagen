build: clean
	go build -o mockagen cmd/mockagen/main.go

clean:
	rm -rf mockagen

test:
	go test ./...

# Run the benchmarks once against the working tree.
bench:
	go test ./cmd/mockagen -run='^$$' -bench=. -benchmem -count=1

# Compare the working tree against another commit the same way CI does:
# interleaved on one machine, then benchstat.  Usage: make bench-compare BASE=master
BASE ?= master
bench-compare:
	@command -v benchstat >/dev/null || go install golang.org/x/perf/cmd/benchstat@v0.0.0-20260813145340-fd4a688df892
	rm -rf .bench-base bench-results
	git worktree add -f .bench-base $(BASE)
	BASE_DIR=$(PWD)/.bench-base HEAD_DIR=$(PWD) OUT_DIR=$(PWD)/bench-results \
		ROUNDS=$${ROUNDS:-6} bash scripts/bench-ab.sh
	benchstat -format=csv base=bench-results/base.txt head=bench-results/head.txt \
		2>/dev/null > bench-results/compare.csv
	go run scripts/bench-report.go --csv bench-results/compare.csv \
		--base-label "$(BASE)" --head-label "$$(git rev-parse --short HEAD)"
	git worktree remove --force .bench-base

.PHONY: build clean test bench bench-compare
