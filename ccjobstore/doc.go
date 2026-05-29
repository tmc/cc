// Package ccjobstore reads background job state and timelines written by the
// Claude Code daemon under ~/.claude/jobs/<shortId>/.
//
// A [Job] captures a background run's status, timing, and metrics; a
// [JobTimelineEvent] records a milestone within it. [ListJobs] and [ReadJob]
// read from the default jobs directory, while [ListJobsIn] and the *From
// variants read from an explicit root.
//
//	jobs, err := ccjobstore.ListJobs()
//	if err != nil {
//		log.Fatal(err)
//	}
//	for _, j := range jobs {
//		fmt.Println(j.ShortID, j.State)
//	}
package ccjobstore
