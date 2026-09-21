#[cfg(test)]
mod test {
    extern crate std;

    use crate::orchestrated_agreement::{
        AgreementState, DIDMethod, OrchestratedAgreementContract,
        OrchestratedAgreementContractClient,
    };
    use crate::zk_verifier::ZKVerifier;
    use soroban_sdk::{
        contract, contractimpl, testutils::Address as _, Address, BytesN, Env, String,
        Symbol, Vec,
    };

    /// Mock verifier contract that always rejects proofs.
    #[contract]
    struct RejectingVerifier;

    #[contractimpl]
    impl RejectingVerifier {
        pub fn verify_proof(_e: &Env, _proof_hash: BytesN<32>) -> bool {
            false
        }
    }

    /// Deploy the OA contract and return (env, client, owner).
    fn setup() -> (Env, OrchestratedAgreementContractClient<'static>, Address) {
        let env = Env::default();
        let owner = Address::generate(&env);
        env.mock_all_auths();

        let contract_id = env.register(OrchestratedAgreementContract, (owner.clone(),));
        let client = OrchestratedAgreementContractClient::new(&env, &contract_id);

        (env, client, owner)
    }

    /// Setup with a DID registry deployed and wired into the OA contract.
    fn setup_with_registry() -> (Env, OrchestratedAgreementContractClient<'static>, Address, Address) {
        let (env, client, owner) = setup();
        let registry_id = env.register(did_registry::DIDRegistryContract, (owner.clone(),));
        client.set_did_registry(&registry_id);
        (env, client, owner, registry_id)
    }

    /// Register a DID in the registry (test helper).
    fn register_did(env: &Env, registry_id: &Address, did: &str, controller: &Address) {
        let client = did_registry::DIDRegistryContractClient::new(env, registry_id);
        let did_str = String::from_str(env, did);
        let mut vms: Vec<did_registry::VerificationMethodEntry> = Vec::new(env);
        vms.push_back(did_registry::VerificationMethodEntry {
            id: String::from_str(env, &std::format!("{}#keys-1", did)),
            vm_type: Symbol::new(env, "Ed25519VerificationKey2020"),
            controller: did_str.clone(),
            public_key_multibase: String::from_str(env, "z6MkTest"),
        });
        client.register_did(
            &did_str,
            &did_registry::DIDMethod::Web,
            controller,
            &vms,
            &Vec::new(env),
        );
    }

    /// Drive an agreement to Funded state (identity + wallet + asset layers).
    fn funded_agreement(
        env: &Env,
        client: &OrchestratedAgreementContractClient,
        initiator: &Address,
        did: &str,
        attestation_hash: &BytesN<32>,
    ) -> BytesN<32> {
        let id = client.create_agreement(initiator, &None, &None, &86400);
        client.attest_identity(
            &id,
            &String::from_str(env, did),
            &DIDMethod::Web,
            &Symbol::new(env, "national_id"),
            attestation_hash,
            &None,
        );
        client.connect_wallet(
            &id,
            initiator,
            &Symbol::new(env, "stellar"),
            &false,
            &1,
        );
        client.fund_and_set_asset(
            &id,
            &Symbol::new(env, "USDC"),
            &1000,
            &500,
            &Symbol::new(env, "fungible"),
            &None,
        );
        id
    }

    #[test]
    fn test_constructor_sets_owner() {
        let (_env, client, owner) = setup();
        assert_eq!(client.owner(), owner);
    }

    #[test]
    fn test_create_agreement() {
        let (_env, client, _owner) = setup();
        let initiator = Address::generate(&client.env);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        assert!(client.agreement_exists(&id));
        assert_eq!(client.get_agreement_count(), 1);

        let agreement = client.get_agreement(&id);
        assert_eq!(agreement.state, AgreementState::Draft);
        assert_eq!(agreement.initiator, initiator);
    }

    #[test]
    fn test_attest_identity() {
        let (env, client, _owner) = setup();
        let initiator = Address::generate(&env);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:example.com"),
            &DIDMethod::Web,
            &Symbol::new(&env, "national_id"),
            &BytesN::from_array(&env, &[1u8; 32]),
            &None,
        );

        let agreement = client.get_agreement(&id);
        assert_eq!(agreement.state, AgreementState::Active);
        assert!(!agreement.identity.is_empty());
    }

    // ─── DID Registry Integration ─────────────

    #[test]
    fn test_set_and_get_did_registry() {
        let (env, client, _owner) = setup();
        let registry_id = env.register(did_registry::DIDRegistryContract, (Address::generate(&env),));

        assert_eq!(client.get_did_registry(), None);
        client.set_did_registry(&registry_id);
        assert_eq!(client.get_did_registry(), Some(registry_id));
    }

    #[test]
    #[should_panic(expected = "#601")]
    fn test_attest_unregistered_did_fails() {
        let (env, client, _owner, _registry) = setup_with_registry();
        let initiator = Address::generate(&env);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        // DID is not registered in the registry → DIDNotRegistered (601)
        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:unregistered.example"),
            &DIDMethod::Web,
            &Symbol::new(&env, "national_id"),
            &BytesN::from_array(&env, &[1u8; 32]),
            &None,
        );
    }

    #[test]
    fn test_attest_registered_did_succeeds() {
        let (env, client, _owner, registry_id) = setup_with_registry();
        let initiator = Address::generate(&env);

        register_did(&env, &registry_id, "did:web:alice.example", &initiator);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:alice.example"),
            &DIDMethod::Web,
            &Symbol::new(&env, "national_id"),
            &BytesN::from_array(&env, &[2u8; 32]),
            &None,
        );

        let agreement = client.get_agreement(&id);
        assert_eq!(agreement.state, AgreementState::Active);
    }

    #[test]
    fn test_attest_unanchored_credential_allowed() {
        let (env, client, _owner, registry_id) = setup_with_registry();
        let initiator = Address::generate(&env);

        register_did(&env, &registry_id, "did:web:bob.example", &initiator);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        // attestation_hash is not an anchored credential — lenient, succeeds
        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:bob.example"),
            &DIDMethod::Web,
            &Symbol::new(&env, "kyc"),
            &BytesN::from_array(&env, &[9u8; 32]),
            &None,
        );

        let agreement = client.get_agreement(&id);
        assert_eq!(agreement.state, AgreementState::Active);
    }

    #[test]
    #[should_panic(expected = "#602")]
    fn test_attest_revoked_credential_fails() {
        let (env, client, _owner, registry_id) = setup_with_registry();
        let initiator = Address::generate(&env);
        let reg = did_registry::DIDRegistryContractClient::new(&env, &registry_id);

        register_did(&env, &registry_id, "did:web:carol.example", &initiator);

        let vc_hash = BytesN::from_array(&env, &[7u8; 32]);
        reg.anchor_credential(
            &vc_hash,
            &String::from_str(&env, "did:web:carol.example"),
            &String::from_str(&env, "did:web:carol.example"),
            &Symbol::new(&env, "national_id"),
        );
        reg.revoke_credential(&vc_hash);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        // Credential is revoked → CredentialRevoked (602)
        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:carol.example"),
            &DIDMethod::Web,
            &Symbol::new(&env, "national_id"),
            &vc_hash,
            &None,
        );
    }

    #[test]
    #[should_panic(expected = "#603")]
    fn test_attest_verifier_rejects() {
        let (env, client, _owner) = setup();
        let initiator = Address::generate(&env);
        let verifier_id = env.register(RejectingVerifier, ());

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        // Verifier returns false → VerifierRejected (603)
        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:dave.example"),
            &DIDMethod::Web,
            &Symbol::new(&env, "national_id"),
            &BytesN::from_array(&env, &[3u8; 32]),
            &Some(verifier_id),
        );
    }

    #[test]
    fn test_attest_verifier_accepts() {
        let (env, client, _owner) = setup();
        let initiator = Address::generate(&env);
        let verifier_id = env.register(ZKVerifier, ());

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        // zk_verifier stub returns true → attestation succeeds
        client.attest_identity(
            &id,
            &String::from_str(&env, "did:web:eve.example"),
            &DIDMethod::Web,
            &Symbol::new(&env, "national_id"),
            &BytesN::from_array(&env, &[4u8; 32]),
            &Some(verifier_id),
        );

        let agreement = client.get_agreement(&id);
        assert_eq!(agreement.state, AgreementState::Active);
    }

    #[test]
    #[should_panic(expected = "#602")]
    fn test_execute_fails_after_credential_revoked() {
        let (env, client, _owner, registry_id) = setup_with_registry();
        let initiator = Address::generate(&env);
        let reg = did_registry::DIDRegistryContractClient::new(&env, &registry_id);

        register_did(&env, &registry_id, "did:web:frank.example", &initiator);

        let vc_hash = BytesN::from_array(&env, &[8u8; 32]);
        reg.anchor_credential(
            &vc_hash,
            &String::from_str(&env, "did:web:frank.example"),
            &String::from_str(&env, "did:web:frank.example"),
            &Symbol::new(&env, "national_id"),
        );

        let id = funded_agreement(&env, &client, &initiator, "did:web:frank.example", &vc_hash);

        // Revoke the credential after funding — execute must fail
        reg.revoke_credential(&vc_hash);

        client.execute_agreement(&id, &BytesN::from_array(&env, &[5u8; 32]));
    }

    #[test]
    fn test_execute_succeeds_with_valid_credential() {
        let (env, client, _owner, registry_id) = setup_with_registry();
        let initiator = Address::generate(&env);
        let reg = did_registry::DIDRegistryContractClient::new(&env, &registry_id);

        register_did(&env, &registry_id, "did:web:grace.example", &initiator);

        let vc_hash = BytesN::from_array(&env, &[6u8; 32]);
        reg.anchor_credential(
            &vc_hash,
            &String::from_str(&env, "did:web:grace.example"),
            &String::from_str(&env, "did:web:grace.example"),
            &Symbol::new(&env, "national_id"),
        );

        let id = funded_agreement(&env, &client, &initiator, "did:web:grace.example", &vc_hash);

        client.execute_agreement(&id, &BytesN::from_array(&env, &[5u8; 32]));

        let agreement = client.get_agreement(&id);
        assert_eq!(agreement.state, AgreementState::Executed);
    }

    // ─── did:soroban ──────────────────────────

    #[test]
    fn test_register_soroban_did() {
        let (env, _client, _owner, registry_id) = setup_with_registry();
        let reg = did_registry::DIDRegistryContractClient::new(&env, &registry_id);

        let account = Address::generate(&env);
        let did = reg.register_soroban_did(&account);

        // DID string is did:soroban:<G-address>
        let expected = std::format!("did:soroban:{}", account.to_string());
        assert_eq!(did, String::from_str(&env, &expected));

        // Resolution returns the derived document
        let doc = reg.resolve_did(&did);
        assert_eq!(doc.method, did_registry::DIDMethod::Soroban);
        assert_eq!(doc.controller, account);
        assert!(!doc.deactivated);
        assert_eq!(doc.verification_methods.len(), 1);
    }

    // ─── Report Anchoring ─────────────────────

    #[test]
    fn test_anchor_report() {
        let (env, client, _owner) = setup();
        let initiator = Address::generate(&env);

        let id = funded_agreement(
            &env,
            &client,
            &initiator,
            "did:web:heidi.example",
            &BytesN::from_array(&env, &[1u8; 32]),
        );

        client.execute_agreement(&id, &BytesN::from_array(&env, &[5u8; 32]));

        let report_hash = BytesN::from_array(&env, &[42u8; 32]);
        client.anchor_report(
            &id,
            &report_hash,
            &Some(String::from_str(&env, "pacs.008.001.08")),
        );

        let agreement = client.get_agreement(&id);
        let reporting = agreement.reporting.first().unwrap();
        assert_eq!(reporting.report_hash, Some(report_hash));
        assert_eq!(
            reporting.iso20022_ref,
            Some(String::from_str(&env, "pacs.008.001.08"))
        );
    }

    #[test]
    #[should_panic(expected = "#604")]
    fn test_anchor_report_before_execute_fails() {
        let (env, client, _owner) = setup();
        let initiator = Address::generate(&env);

        let id = client.create_agreement(&initiator, &None, &None, &86400);

        // No reporting layer yet (agreement not executed) → ReportNotAnchored (604)
        client.anchor_report(&id, &BytesN::from_array(&env, &[42u8; 32]), &None);
    }
}
