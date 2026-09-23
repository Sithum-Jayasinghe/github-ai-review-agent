package agent

// SampleHandler demonstrates a basic request handler pattern.
// This file is used to test the AI PR review agent.
func SampleHandler(input string) (string, error) {
	if input == "" {
		return "", nil
	}
	result := "processed: " + input
	return result, nil
}
