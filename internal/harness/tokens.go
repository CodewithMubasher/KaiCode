package harness

// EstimateTokens approximates how many tokens the provider's model spends on
// s. It is not a real tokenizer: the web chat does not expose its vocabulary.
// Runs of ASCII letters, digits and "_" cost about one token per four
// characters (the usual ratio for English and code); whitespace is nearly
// free; punctuation and non-ASCII runes (CJK, emoji) cost one token each.
// The sidebar uses it for the context gauge, not for billing.
func EstimateTokens(s string) int {
	tokens, run := 0, 0
	flush := func() {
		if run > 0 {
			tokens += (run + 3) / 4
			run = 0
		}
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			run++
		case r == ' ' || r == '\t' || r == '\r' || r == '\n':
			flush()
		default:
			flush()
			tokens++
		}
	}
	flush()
	return tokens
}
