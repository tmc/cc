// Package ccusage normalizes the token accounting a coding-agent session
// reports, so counts from different harnesses can be summed together.
//
// Every harness [cc] can read attaches a usage record to its assistant
// messages, but they do not agree on what the numbers mean. Claude reports
// uncached prompt tokens and cache tokens separately; Codex and Gemini report
// both cached and newly-cached tokens as subsets of input, so adding their
// input to Claude's compares a cache-inclusive number with a cache-exclusive
// one. Claude writes several JSONL entries per API message, each repeating the
// prompt counts, and an entry written before the completion finished carries a
// streaming-start output snapshot instead of the count.
//
// [FromEntries] is the one place those differences are resolved. It turns a
// session's entries into [Record] values in the Anthropic convention: four
// disjoint counts whose sum is the whole request. Callers that want a total
// should go through it rather than summing cc.Usage themselves.
//
// [Weight] reduces a Record to a single number for ranking, in a named unit;
// "cost" weights the four counts with a [Pricing] rate set, which
// [PricingForModel] chooses per model.
package ccusage
