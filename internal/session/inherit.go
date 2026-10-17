package session

// Context inheritance plumbing for the "infinite context" feature (#483, built
// on the header fields added by #480). A session created to *continue* another
// session's collapsed context records two things on its header:
//
//   - ContextFrom: the source session id whose checkpoint holds the collapsed
//     prefix (see runtime.LoadCheckpoint / CheckpointPath).
//   - ContextWatermark: the message index up to which that prefix was collapsed;
//     on resume/fork the runtime (#481) can load the source checkpoint and skip
//     re-summarizing messages [0, ContextWatermark).
//
// This is deliberately side-effect-free header plumbing: it never touches the
// memory dir or the runtime. It sits alongside ParentSession (which records
// lineage) — inheritance additionally records *where the collapsed context came
// from* so a forked/continued session need not re-summarize early context.

