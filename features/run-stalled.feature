Feature: Describe an observed quiet run
  @measured go test ./internal/render -run TestRunStalled 2026-09-19:
  run_stalled rendered as an unknown event without its timing observations.

  # @test:TestRunStalledNamesTheObservationWithoutDiagnosingTheCause
  Scenario: A run stops calling
    Given the control plane reports a stalled run with last-call and silence measurements
    When its notification is rendered
    Then the message names the quiet run, time and duration without diagnosing its cause

  # @test:TestRunStalledRejectsInvalidNumbersWithoutRenderingContent
  Scenario: Timing fields contain invalid values
    Given text, negative, fractional, non-finite or overflowing timing values
    When its notification is rendered
    Then those values are omitted and no producer content reaches the message
