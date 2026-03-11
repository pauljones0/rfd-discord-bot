1. **Understand**
   - The task is to add tests for `CalculateHeatScore` in `internal/notifier/discord.go`.
   - Wait, the issue description states that the code for `CalculateHeatScore` is:
     ```go
     func CalculateHeatScore(likes, comments, views int) float64 {
	// Simple formula prioritizing likes, then comments, then views
	return float64(likes)*1.5 + float64(comments)*0.5 + float64(views)*0.01
     }
     ```
     BUT the codebase actually contains:
     ```go
     func CalculateHeatScore(likes, comments, views int) float64 {
	if views == 0 {
		return 0.0
	}
	// Clamp negatives — downvoted deals shouldn't generate heat
	effectiveLikes := max(likes, 0)
	effectiveComments := max(comments, 0)
	// Comments are weighted 2x since they represent deeper engagement
	engagement := float64(effectiveLikes) + 2.0*float64(effectiveComments)
	return engagement / float64(views)
     }
     ```
   - I will test the actual function present in the codebase.
   - I should use table-driven tests.

2. **Test Cases for CalculateHeatScore**
   - **Zero views:** `likes=10, comments=5, views=0` -> `0.0`
   - **Happy path:** `likes=10, comments=5, views=100` -> `(10 + 2*5) / 100 = 0.2`
   - **Negative likes clamped:** `likes=-5, comments=5, views=100` -> `(0 + 2*5) / 100 = 0.1`
   - **Negative comments clamped:** `likes=10, comments=-5, views=100` -> `(10 + 0) / 100 = 0.1`
   - **Both negative:** `likes=-10, comments=-10, views=100` -> `0.0`
   - **All zeros:** `likes=0, comments=0, views=0` -> `0.0`
   - **Large numbers:** `likes=1000, comments=500, views=10000` -> `(1000 + 1000) / 10000 = 0.2`

3. **Plan**
   - Create a test function `TestCalculateHeatScore` in `internal/notifier/discord_test.go`
   - Implement table-driven tests covering the cases above.
   - Run tests to verify.
   - Complete pre-commit instructions.
   - Submit.
