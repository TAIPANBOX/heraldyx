Feature: After the hourly ceiling is reached the operator keeps hearing what it holds

  From issue #71, measured on an appliance run on 2026-09-17: the ceiling of
  20 an hour was reached at 12:11Z with one summary saying 8 were held. Over
  the next 28 minutes the log gained a critical, ten highs and about twenty
  mediums; every one was read, none was mailed, and no further summary went,
  because the summary was rate limited to one an hour and the critical was
  held with the rest. The asks: a summary again whenever alerts are still
  being held, with the true count and the worst severity since the previous
  one; a critical goes through the ceiling, one message per condition with
  dedup still applying; and the log line at the ceiling says what happens
  next. The ceiling itself and the dedup window are unchanged.

  Scenario: a critical arrives after the ceiling is reached
    Given the ceiling of 20 was reached this hour and five alerts are held
    When a critical event arrives that nothing has been mailed about
    Then it is mailed at once, and the same critical again inside the dedup window is not
  # @test:TestACriticalIsSentThroughTheCeiling

  Scenario: alerts held after the first summary are summarised again
    Given a summary of held alerts went out and three more were held a minute later
    When ten minutes have passed since that summary, with nothing new in the log
    Then a second summary goes out naming three, the time the first of them was held, and their worst severity
  # @test:TestHeldAlertsAreSummarisedAgainWithinABoundedInterval

  Scenario: the summary names the worst severity that was held
    Given the ceiling was filled by medium alerts
    When five more mediums and one high are held
    Then the summary says six were held and the worst was high
  # @test:TestTheSummaryNamesTheWorstSeverityHeld

  Scenario: a large burst is summarised sooner than the interval, but never as a flood
    Given a summary went out two minutes ago
    When fifty more alerts are held in one poll
    Then a summary goes out now, and a further fifty held thirty seconds later wait for the one-minute floor
  # @test:TestALargeBurstIsSummarisedSoonerThanTheInterval

  Scenario: the log says what happens next when the ceiling is reached
    Given twenty messages have gone out this hour
    When the first alert is held back
    Then the log says the ceiling is holding alerts, that a summary follows, and that a critical is still sent, once per summary period rather than once per poll
  # @test:TestTheLogSaysWhatHappensNextAtTheCeiling

  Scenario: a critical bypasses the ceiling and dedup still holds
    Given the ceiling is reached
    When a critical is decided, then the same critical again, then a different critical
    Then the first is notified, the repeat is dropped by dedup, and the different one is notified
  # @test:TestACriticalBypassesTheCeilingAndDedupStillHolds

  Scenario: the summary cadence has a time bound, a burst bound and a floor
    Given alerts are being held since a summary went out
    When ten minutes pass, or fifty are held at least a minute after the previous summary
    Then a summary is due, and it is never due sooner than a minute after the previous one
  # @test:TestTheSummaryCadenceHasBothBoundsAndAFloor

  Scenario: a summary from an older state file still reads
    Given a state file written before the worst severity and the start time were kept
    When the held count in it is summarised
    Then the mail carries the count and says nothing about a worst severity or a start it does not know
  # @test:TestASummaryWithoutAWorstOrAStartStillReads

  Scenario: the ceiling itself is unchanged
    Given a ceiling of five and a thousand distinct high alerts
    When they are decided
    Then five are notified and the rest are held, and the first summary carries all of them
  # @test:TestCeilingHolds

  Scenario: the dedup window is unchanged
    Given one critical condition tripping two hundred times inside the window
    When each trip is decided
    Then one message is sent
  # @test:TestDedupHolds
