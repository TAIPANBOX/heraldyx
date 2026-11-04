Feature: A storm of one dependency's failures is one mail

  A provider that is down fails every call that reaches it, and every call is
  its own run. Keyed on the run, an hour of outage was one mail per call up to
  the hourly ceiling and a summary of the rest. For dependency_failed the dedup
  key follows the dependency that failed and what the gateway did about it, so
  the storm is one mail per window. The event's contract is unchanged and the
  field is read from data the event already carries.

  # @test:TestAStormOfProviderFailuresIsOneMessage
  Scenario: Two hundred failed calls of one provider are one message
    Given two hundred dependency_failed events for different runs, each naming the provider and the effect call_failed
    When they pass the dedup rule within one window
    Then one of them is notified and the rest are dropped

  # @test:TestAStormOfProviderFailuresIsOneMailAndNothingIsHeld
  Scenario: Thirty failed calls read from the log are one mail and nothing is held for a summary
    Given thirty dependency_failed events for different runs in the shared log
    When heraldyx polls the log
    Then one mail is sent and the hourly ceiling holds nothing back

  # @test:TestTheDependencyWindowEndsAndTheProviderCanSpeakAgain
  Scenario: The window ends and the provider can speak again
    Given a provider failure mailed at the start of the window
    When another run fails after the window
    Then it is mailed again

  # @test:TestEachDependencyIsItsOwnCondition
  Scenario: The provider, the policy plane and the control plane are different conditions
    Given a failure of each of the three dependencies
    Then each is mailed

  # @test:TestADifferentEffectOfTheSameDependencyIsNotHiddenBehindTheFirst
  Scenario: Calls that went through with no policy are not hidden behind calls that were refused
    Given a policy plane failing closed and then failing open
    Then both are notified, and the same effect again is dropped

  # @test:TestPolicyPlaneFailuresOfDifferentEffectAreBothMailed
  Scenario: Both effects reach the mailbox through the real loop
    Given policy plane failures of both effects in the shared log
    When heraldyx polls the log
    Then one mail is sent for each effect

  # @test:TestADependencyFailureWithNoUsableDependencyKeepsItsRunKey
  Scenario: A value that is not one of the event's own words folds nothing
    Given dependency_failed events with no data, an unknown dependency, a value that is not text, a path-like or oversized value
    Then each keeps the key of its run and two runs are two mails

  # @test:TestAnUnknownEffectLeavesTheEffectOutOfTheKey
  Scenario: An effect this build does not know leaves the effect out of the key
    Given a provider failure whose effect is a word this build does not know
    Then the key names the dependency alone

  # @test:TestOnlyDependencyFailedFoldsOnTheDependency
  Scenario: Another type that carries the same field does not borrow the rule
    Given a policy_deny event whose data names a dependency
    Then its key is still the type and the run

  # @test:TestTheKeyOfEveryOtherEventIsStillTheTypeAndTheRun
  Scenario: Every other event keeps its key
    Given a budget_exhausted event and a run_stalled event with no run
    Then their keys are the type and the run, or the agent when there is no run

  # @test:TestTheDependencyKeyCanBeAcknowledgedAndHoldsTheStorm
  Scenario: Acknowledging a provider outage holds the whole storm
    Given the key the mail prints for a provider failure
    When an operator acknowledges it
    Then the key is accepted and every failure of that provider is held

  # @test:TestTheIncidentIDIsStillTheTypeAndTheRun
  Scenario: The incident id is still the type and the run
    Given a dependency_failed event for a run
    Then its incident id is the type and the run

  # @test:TestTheLinkOfADependencyFailureStillNamesTheRun
  Scenario: The link in the mail does not move
    Given a dependency_failed event for a run
    Then the link in the mail still points at the incident filed under the type and the run
