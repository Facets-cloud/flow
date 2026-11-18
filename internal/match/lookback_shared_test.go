package match

import (
	"fmt"
	"strings"
)

// synthLookback builds a small corpus with topic clusters linked by project
// and mentions. Shared test fixture: used by the confidence tests (default
// build) and the eval tests (behind the recalleval tag), so it lives in an
// untagged test file.
func synthLookback() []TaskData {
	topics := []string{"kafka broker partition rebalance", "postgres logical replication slot", "invoice billing customer payment", "terraform module provider output"}
	var tasks []TaskData
	for i := 0; i < 40; i++ {
		tp := i % len(topics)
		slug := fmt.Sprintf("topic%d-task-%02d", tp, i)
		td := TaskData{Slug: slug, Name: fmt.Sprintf("work item %d on %s", i, strings.Fields(topics[tp])[0]),
			CreatedAt: fmt.Sprintf("2026-%02d-%02dT10:00:00Z", 1+i/28, 1+i%28), Status: "done",
			Project: fmt.Sprintf("proj%d", tp),
			Brief:   "Goal: " + topics[tp] + " cleanup and checks. Contact the owner about " + topics[tp] + "."}
		if i >= 4 {
			prev := fmt.Sprintf("topic%d-task-%02d", tp, i-4)
			td.Brief += " Continues " + prev + " from last week."
		}
		td.Updates = []Update{{Date: td.CreatedAt[:10], Text: "progress on " + topics[tp]}}
		tasks = append(tasks, td)
	}
	return tasks
}
