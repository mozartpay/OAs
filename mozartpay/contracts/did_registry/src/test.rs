#[cfg(test)]
mod test {
    use crate::{DIDMethod, DIDRegistryContract, DIDRegistryContractClient};
    use crate::{CredentialStatus, ServiceEntry, VerificationMethodEntry};
    use soroban_sdk::{testutils::Address as _, vec, Address, BytesN, Env, String, Symbol};

    fn setup() -> (Env, DIDRegistryContractClient<'static>, Address) {
        let env = Env::default();
        env.mock_all_auths();

        let owner = Address::generate(&env);
        let contract_id = env.register(DIDRegistryContract, (&owner,));
        let client = DIDRegistryContractClient::new(&env, &contract_id);

        (env, client, owner)
    }

    fn sample_vm(env: &Env, did: &str, vm_id: &str) -> VerificationMethodEntry {
        VerificationMethodEntry {
            id: String::from_str(env, vm_id),
            vm_type: Symbol::new(env, "Ed25519VerificationKey2020"),
            controller: String::from_str(env, did),
            public_key_multibase: String::from_str(env, "z6MkmJ6yNbSaefPjWbNDj6tfbVTvmV6utze1fF5CxXB5fGk5"),
        }
    }

    fn register_sample(env: &Env, client: &DIDRegistryContractClient, controller: &Address, did: &str) {
        let vms = vec![env, sample_vm(env, did, "did:test#keys-1")];
        let services = vec![
            env,
            ServiceEntry {
                id: String::from_str(env, "did:test#svc"),
                svc_type: Symbol::new(env, "LinkedDomains"),
                endpoint: String::from_str(env, "https://example.com"),
            },
        ];
        client.register_did(
            &String::from_str(env, did),
            &DIDMethod::Key,
            controller,
            &vms,
            &services,
        );
    }

    #[test]
    fn test_constructor_sets_owner() {
        let (_env, client, owner) = setup();
        assert_eq!(client.owner(), owner);
    }

    #[test]
    fn test_register_and_resolve() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "did:key:z6MkTest");

        assert!(client.did_exists(&String::from_str(&env, "did:key:z6MkTest")));
        assert_eq!(client.get_did_count(), 1);

        let doc = client.resolve_did(&String::from_str(&env, "did:key:z6MkTest"));
        assert_eq!(doc.controller, controller);
        assert_eq!(doc.method, DIDMethod::Key);
        assert_eq!(doc.verification_methods.len(), 1);
        assert_eq!(doc.services.len(), 1);
        assert!(!doc.deactivated);

        let dids = client.get_controller_dids(&controller);
        assert_eq!(dids.len(), 1);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #202)")]
    fn test_register_duplicate_fails() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "did:key:z6MkDup");
        register_sample(&env, &client, &controller, "did:key:z6MkDup");
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #300)")]
    fn test_register_invalid_did_fails() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "not-a-did");
    }

    #[test]
    fn test_update_did() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "did:web:example.com");

        let new_vms = vec![
            &env,
            sample_vm(&env, "did:web:example.com", "did:web:example.com#keys-1"),
            VerificationMethodEntry {
                id: String::from_str(&env, "did:web:example.com#keys-2"),
                vm_type: Symbol::new(&env, "JsonWebKey2020"),
                controller: String::from_str(&env, "did:web:example.com"),
                public_key_multibase: String::from_str(&env, "z6MkSecondKey"),
            },
        ];

        client.update_did(
            &String::from_str(&env, "did:web:example.com"),
            &new_vms,
            &vec![&env],
        );

        let doc = client.resolve_did(&String::from_str(&env, "did:web:example.com"));
        assert_eq!(doc.verification_methods.len(), 2);
        assert_eq!(doc.services.len(), 0);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #203)")]
    fn test_deactivate_then_resolve_fails() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "did:key:z6MkDead");
        client.deactivate_did(&String::from_str(&env, "did:key:z6MkDead"));

        // Still exists but resolution fails
        assert!(client.did_exists(&String::from_str(&env, "did:key:z6MkDead")));
        client.resolve_did(&String::from_str(&env, "did:key:z6MkDead"));
    }

    #[test]
    fn test_anchor_and_revoke_credential() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "did:key:z6MkIssuer");

        let vc_hash = BytesN::from_array(&env, &[7u8; 32]);
        client.anchor_credential(
            &vc_hash,
            &String::from_str(&env, "did:key:z6MkIssuer"),
            &String::from_str(&env, "did:key:z6MkSubject"),
            &Symbol::new(&env, "NationalIdentityCredential"),
        );

        let rec = client.credential_status(&vc_hash);
        assert_eq!(rec.status, CredentialStatus::Valid);
        assert_eq!(rec.revoked_at, None);

        client.revoke_credential(&vc_hash);

        let rec = client.credential_status(&vc_hash);
        assert_eq!(rec.status, CredentialStatus::Revoked);
        assert!(rec.revoked_at.is_some());
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #204)")]
    fn test_credential_status_not_found() {
        let (env, client, _owner) = setup();
        let vc_hash = BytesN::from_array(&env, &[9u8; 32]);
        client.credential_status(&vc_hash);
    }

    #[test]
    #[should_panic(expected = "Error(Contract, #206)")]
    fn test_double_revoke_fails() {
        let (env, client, _owner) = setup();
        let controller = Address::generate(&env);

        register_sample(&env, &client, &controller, "did:key:z6MkIssuer2");

        let vc_hash = BytesN::from_array(&env, &[3u8; 32]);
        client.anchor_credential(
            &vc_hash,
            &String::from_str(&env, "did:key:z6MkIssuer2"),
            &String::from_str(&env, "did:key:z6MkSubject"),
            &Symbol::new(&env, "KYCCredential"),
        );
        client.revoke_credential(&vc_hash);
        client.revoke_credential(&vc_hash);
    }
}
