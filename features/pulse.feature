Feature: A quiet box still tells its operator once a day that the notifier is alive

  @decided 2026-10-15: borrowed from monitoring systems that mark a result
  stale when it stops arriving. Before this, the daily digest was skipped
  when nothing fell below the alert line, so a box where nothing happened and
  a box whose notifier had died both sent nothing at all. The pulse is one
  mail per period with the numbers this process saw; its absence is the
  signal. HERALDYX_PULSE_HOURS sets the period, 0 turns it off.

  Scenario: nothing happened all day
    Given a box where no event arrived for a whole pulse period
    When the period ends
    Then exactly one pulse mail goes out, and none again inside the next period
  # @test:TestAQuietBoxStillSaysItIsAliveOnceADay

  Scenario: the digest is due at the same time
    Given something below the alert line was seen during the day
    When the digest goes out
    Then it carries the pulse lines and no separate pulse mail is sent
  # @test:TestThePulseRidesOnTheDigestRatherThanASecondMail

  Scenario: the operator turned the pulse off
    Given HERALDYX_PULSE_HOURS is 0
    When two days pass with nothing happening
    Then no pulse mail goes out
  # @test:TestThePulseCanBeTurnedOff

  Scenario: a restart in the middle of the day
    Given the pulse window has been counting events
    When the process restarts
    Then the counters and the window start are still there
  # @test:TestPulseCountersSurviveARestart
