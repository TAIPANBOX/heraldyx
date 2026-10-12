Feature: tokenfuse's retained calls and their closures are told to a person in plain words

  tokenfuse retains a call it handed to the provider when the call ended before
  any answer came back: the money stays reserved until a person closes it with
  evidence (agent-passport SPEC.md 6.2, agent-passport#74). @decided 2026-10-12:
  both events go on the bus so a person is told; call_retained (medium) asks a
  person to act soon, retained_closed (high) is high because it lifts a money
  control. heraldyx describes both instead of mailing them as the generic
  fallback. The mail says which run, how much is held and whether it survives a
  restart or can be closed from the console; a closure says which reservation,
  which kind of evidence and what was settled. Also @decided 2026-10-12: only
  the three numbers (reservation_id, amount_microusd, settled_microusd) may
  reach a mail, and the evidence reference never does, in any form, because it
  is free text typed in the console and tokenfuse lets a model propose a
  closure with its evidence for a person to accept. The mail says where it is.

  Scenario: both types have a sentence
    Given a call_retained and a retained_closed event as tokenfuse writes them
    When each is rendered
    Then neither falls through to "raised an event this build does not have a description for"
  # @test:TestEveryRetainedTypeIsDescribed

  Scenario: a retained call names the run, the amount held and who must close it
    Given a call_retained event for agent X on run-42 holding 420000 micro-dollars, journaled and listed
    Then the subject and the first sentence say a call was sent and never answered and $0.42 stays reserved until a person closes it, the mail names the reservation and the model, says nothing was charged or released, and says it survives a restart
  # @test:TestCallRetainedSaysTheCallWasSentAndNeverAnsweredAndWhatStaysReserved

  Scenario: a reservation that never reached the journal is said to be lost on a restart
    Given a call_retained event with journaled false
    Then the mail says it did not reach the gateway's journal and a restart forgets it
  # @test:TestCallRetainedNotJournaledSaysARestartForgetsIt

  Scenario: a reservation off the console's list is said to be impossible to close there
    Given a call_retained event with listed false
    Then the mail says it cannot be closed from the console and the money is still held
  # @test:TestCallRetainedNotListedSaysItCannotBeClosedFromTheConsole

  Scenario: an event that does not say journaled or listed gets neither claim
    Given a call_retained event with journaled and listed absent, or not booleans
    Then the mail claims neither that it survives a restart, nor that a restart forgets it, nor that it cannot be closed
  # @test:TestCallRetainedWithoutJournaledOrListedClaimsNeither

  Scenario: an amount under a cent is not shown as nothing
    Given a call_retained event holding 2500 micro-dollars
    Then the subject says $0.0025 stays reserved, never $0.00
  # @test:TestASubCentReservationIsWrittenOutExactly

  Scenario: an observed closure names the reservation, the evidence and the amount
    Given a retained_closed event for reservation 1234 on run-42, observed, settling 420000 micro-dollars, evidence INV-2026-4411
    Then the subject and the first sentence say retained reservation 1234 was closed by a person as observed, settling $0.42, the mail says the evidence reference is in the operator console with the console.close_retained record without printing it, says a money control was lifted, says who closed it is not in the event and points at console.close_retained, and prints neither the revision nor the operation id; a reservation id written as a plain identifier string is named as written
  # @test:TestRetainedClosedObservedNamesTheReservationTheEvidenceAndTheAmount

  Scenario: a no-charge closure says nothing was charged and the hold was released
    Given a retained_closed event with evidence kind no_charge settling 0
    Then the subject says closed by a person as no charge, settling $0.00, and the mail says nothing is charged and what was held is released
  # @test:TestRetainedClosedNoChargeSaysNothingWasChargedAndTheHoldWasReleased

  Scenario: an evidence kind this build does not know stays neutral
    Given a retained_closed event whose evidence kind is not observed or no_charge, with a line break and a forged header in it
    Then the mail says the reservation was closed by a person and the amount settled, and neither prints the kind nor guesses one
  # @test:TestRetainedClosedUnknownEvidenceKindStaysNeutral

  Scenario: an evidence reference of any content is never mailed
    Given retained_closed events whose evidence reference is a plain identifier, a sentence a model could have written, a line break and a forged header, 5000 characters, markup, a link, a bare host and path, a right-to-left override, terminal escapes, invalid UTF-8, a number or an object
    Then none of it appears in the subject or the body, the mail says the evidence reference is in the operator console with the console.close_retained record, and the closure itself is still said in full
  # @test:TestTheEvidenceRefIsNeverMailed

  Scenario: a hostile model or reservation id never reaches the mail
    Given call_retained and retained_closed events whose model or reservation id carries a line break, is huge, is markup, a sentence, a link, an override, an object, negative, fractional or too large
    Then none of it is rendered and the retained call or the closure is still said
  # @test:TestHostileModelAndReservationIDNeverReachTheMail

  Scenario: a hostile amount is never rendered
    Given call_retained and retained_closed events whose amount is negative, fractional, huge, text or an object
    Then no amount is printed and the mail falls back to saying the money stays reserved, or the closure without a settled figure
  # @test:TestHostileAmountsAreNeverRendered

  Scenario: another plane's retained keys are not made renderable
    Given a budget_threshold event whose data carries reservation_id, amount_microusd, settled_microusd and evidence_ref
    Then none of them is rendered
  # @test:TestRetainedKeysAreRenderedOnlyForTheRetainedTypes

  Scenario: the allowlist grows only by the three numbers the sentences print
    Given the data allowlist
    Then reservation_id, amount_microusd, settled_microusd and model are in it, and evidence_ref, journaled, listed, evidence_kind, revision, operation_id and unit are not
  # @test:TestOnlyTheRetainedKeysTheSentencesPrintJoinTheAllowlist

  Scenario: a retained value reaches the mail only through the allowlist
    Given each value a retained sentence prints, first shown in the mail
    When its key is taken out of the allowlist
    Then the value is gone from the mail
  # @test:TestRetainedValuesReachTheMailOnlyThroughTheAllowlist

  Scenario: the catalog describes fifty types
    Given the event-type catalog
    Then it holds 50 entries, 48 before tokenfuse's two retained types
  # @test:TestTheCatalogDescribesFiftyTypes
