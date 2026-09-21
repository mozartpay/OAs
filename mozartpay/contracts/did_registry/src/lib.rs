// SPDX-License-Identifier: MIT
// Compatible with OpenZeppelin Stellar Soroban Contracts ^0.6.0
//
// DID Registry — standalone, reusable on-chain DID document registry and
// verifiable credential status list for the Stellar/Soroban ecosystem.
//
// Stores full DID documents (verification methods + service endpoints) so
// `resolve_did` returns a complete, on-chain-resolvable document. Also tracks
// credential status (Valid/Revoked) keyed by credential content hash.

#![no_std]

use soroban_sdk::{
    contract, contracterror, contractimpl, contracttype,
    panic_with_error, Address, Bytes, BytesN, Env, String, Symbol, Vec,
};

// ─────────────────────────────────────────────
// Custom Errors
// ─────────────────────────────────────────────

#[contracterror]
#[derive(Copy, Clone, Debug, Eq, PartialEq, PartialOrd, Ord)]
#[repr(u32)]
pub enum DIDRegistryError {
    // Authorization errors (1xx)
    Unauthorized = 100,
    NotController = 101,
    NotOwner = 102,

    // State errors (2xx)
    ContractPaused = 200,
    DIDNotFound = 201,
    DIDAlreadyExists = 202,
    DIDDeactivated = 203,
    CredentialNotFound = 204,
    CredentialAlreadyAnchored = 205,
    AlreadyRevoked = 206,

    // Input validation errors (3xx)
    InvalidDID = 300,
    InvalidHash = 301,
    EmptyVerificationMethods = 302,
}

// ─────────────────────────────────────────────
// Contract State & Types
// ─────────────────────────────────────────────

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DIDMethod {
    Web,
    Key,
    Ethr,
    Ebsi,
    Soroban,
}

/// A single verification method entry in a DID document.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct VerificationMethodEntry {
    pub id: String,                   // e.g. did:example:123#keys-1
    pub vm_type: Symbol,              // Ed25519VerificationKey2020, JsonWebKey2020, ...
    pub controller: String,           // DID of the controller
    pub public_key_multibase: String, // multibase-encoded public key
}

/// A service endpoint entry in a DID document.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ServiceEntry {
    pub id: String,          // e.g. did:example:123#linkeddomains
    pub svc_type: Symbol,    // LinkedDomains, DIDCommMessaging, ...
    pub endpoint: String,    // URL or URI
}

/// Full DID document stored on-chain.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct DIDDocumentData {
    pub did: String,
    pub method: DIDMethod,
    pub controller: Address, // Stellar address authorized to update/deactivate
    pub verification_methods: Vec<VerificationMethodEntry>,
    pub services: Vec<ServiceEntry>,
    pub created_at: u64,
    pub updated_at: u64,
    pub deactivated: bool,
}

/// Lifecycle status of an anchored credential.
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum CredentialStatus {
    Valid,
    Revoked,
}

/// On-chain credential status record (content-addressed by VC hash).
#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CredentialRecord {
    pub vc_hash: BytesN<32>,       // SHA-256 of canonical VC JSON
    pub issuer_did: String,
    pub subject_did: String,
    pub vc_type: Symbol,
    pub status: CredentialStatus,
    pub anchored_at: u64,
    pub revoked_at: Option<u64>,
}

#[contracttype]
#[derive(Clone, Debug, Eq, PartialEq)]
pub enum DataKey {
    Owner,
    Paused,
    DIDCount,
    DID(String),                 // DID string -> DIDDocumentData
    Credential(BytesN<32>),      // VC hash -> CredentialRecord
    ControllerDIDs(Address),     // controller -> list of DID strings
}

// ─────────────────────────────────────────────
// Events
// ─────────────────────────────────────────────

pub fn emit_did_registered(e: &Env, did: &String, controller: &Address, timestamp: u64) {
    e.events().publish(
        (Symbol::new(e, "did_registered"), did.clone()),
        (controller.clone(), timestamp),
    );
}

pub fn emit_did_updated(e: &Env, did: &String, timestamp: u64) {
    e.events().publish(
        (Symbol::new(e, "did_updated"), did.clone()),
        timestamp,
    );
}

pub fn emit_did_deactivated(e: &Env, did: &String, timestamp: u64) {
    e.events().publish(
        (Symbol::new(e, "did_deactivated"), did.clone()),
        timestamp,
    );
}

pub fn emit_vc_anchored(e: &Env, vc_hash: &BytesN<32>, issuer_did: &String, timestamp: u64) {
    e.events().publish(
        (Symbol::new(e, "vc_anchored"), vc_hash.clone()),
        (issuer_did.clone(), timestamp),
    );
}

pub fn emit_vc_revoked(e: &Env, vc_hash: &BytesN<32>, timestamp: u64) {
    e.events().publish(
        (Symbol::new(e, "vc_revoked"), vc_hash.clone()),
        timestamp,
    );
}

// ─────────────────────────────────────────────
// Contract Implementation
// ─────────────────────────────────────────────

#[contract]
pub struct DIDRegistryContract;

#[contractimpl]
impl DIDRegistryContract {
    // ─── Constructor & Admin ──────────────────

    /// Constructor - initializes the owner and pause state.
    pub fn __constructor(e: &Env, owner: Address) {
        e.storage().instance().set(&DataKey::Owner, &owner);
        e.storage().instance().set(&DataKey::Paused, &false);
        e.storage().instance().set(&DataKey::DIDCount, &0u64);
    }

    /// Transfer ownership to new address (owner only)
    pub fn transfer_ownership(e: &Env, new_owner: Address) {
        Self::require_owner(e);
        let current_owner: Address = e.storage().instance().get(&DataKey::Owner).expect("Owner not set");
        if new_owner == current_owner {
            panic_with_error!(e, DIDRegistryError::Unauthorized);
        }
        e.storage().instance().set(&DataKey::Owner, &new_owner);
    }

    /// Get current owner
    pub fn owner(e: &Env) -> Address {
        e.storage().instance().get(&DataKey::Owner).expect("Owner not set")
    }

    /// Pause contract operations (owner only)
    pub fn pause(e: &Env) {
        Self::require_owner(e);
        e.storage().instance().set(&DataKey::Paused, &true);
    }

    /// Unpause contract operations (owner only)
    pub fn unpause(e: &Env) {
        Self::require_owner(e);
        e.storage().instance().set(&DataKey::Paused, &false);
    }

    /// Check if contract is paused
    pub fn is_paused(e: &Env) -> bool {
        e.storage().instance().get(&DataKey::Paused).unwrap_or(false)
    }

    // ─── DID Registry ─────────────────────────

    /// Register a new DID document on-chain.
    ///
    /// # Arguments
    /// * `did` - Full DID string (e.g. did:web:example.com, did:key:z6Mk...)
    /// * `method` - DID method enum
    /// * `controller` - Stellar address authorized to manage this DID (must authenticate)
    /// * `verification_methods` - Verification method entries
    /// * `services` - Service endpoint entries (may be empty)
    pub fn register_did(
        e: &Env,
        did: String,
        method: DIDMethod,
        controller: Address,
        verification_methods: Vec<VerificationMethodEntry>,
        services: Vec<ServiceEntry>,
    ) {
        Self::require_not_paused(e);
        controller.require_auth();

        Self::validate_did(e, &did);

        if verification_methods.is_empty() {
            panic_with_error!(e, DIDRegistryError::EmptyVerificationMethods);
        }

        Self::do_register(e, did, method, controller, verification_methods, services);
    }

    /// Register a `did:soroban` DID derived from a Stellar account address.
    /// The DID string is `did:soroban:<G-address>`, the controller is the
    /// account itself, and a single Ed25519 verification method is derived
    /// from the account key. The account must authenticate.
    ///
    /// # Arguments
    /// * `account` - Stellar account address (G...) to derive the DID from
    pub fn register_soroban_did(e: &Env, account: Address) -> String {
        Self::require_not_paused(e);
        account.require_auth();

        // Derive DID string: did:soroban:<strkey>
        let addr_str = account.to_string();
        let mut did_bytes = Bytes::from_slice(e, b"did:soroban:");
        did_bytes.append(&addr_str.to_bytes());
        let did = did_bytes.to_string();

        // Verification method id: <did>#keys-1
        let mut vm_id_bytes = did_bytes.clone();
        vm_id_bytes.append(&Bytes::from_slice(e, b"#keys-1"));
        let vm_id = vm_id_bytes.to_string();

        let mut vms: Vec<VerificationMethodEntry> = Vec::new(e);
        vms.push_back(VerificationMethodEntry {
            id: vm_id,
            vm_type: Symbol::new(e, "Ed25519VerificationKey2020"),
            controller: did.clone(),
            // The strkey G-address encodes the ed25519 public key.
            public_key_multibase: addr_str,
        });

        Self::do_register(
            e,
            did.clone(),
            DIDMethod::Soroban,
            account,
            vms,
            Vec::new(e),
        );

        did
    }

    /// Resolve a DID to its full on-chain document.
    /// Panics with DIDNotFound if unregistered, DIDDeactivated if tombstoned.
    pub fn resolve_did(e: &Env, did: String) -> DIDDocumentData {
        let doc = Self::get_did_document(e, &did);
        if doc.deactivated {
            panic_with_error!(e, DIDRegistryError::DIDDeactivated);
        }
        doc
    }

    /// Update verification methods and service endpoints for a DID.
    /// Only the registered controller may update.
    pub fn update_did(
        e: &Env,
        did: String,
        verification_methods: Vec<VerificationMethodEntry>,
        services: Vec<ServiceEntry>,
    ) {
        Self::require_not_paused(e);
        let mut doc = Self::get_did_document(e, &did);
        doc.controller.require_auth();

        if doc.deactivated {
            panic_with_error!(e, DIDRegistryError::DIDDeactivated);
        }
        if verification_methods.is_empty() {
            panic_with_error!(e, DIDRegistryError::EmptyVerificationMethods);
        }

        doc.verification_methods = verification_methods;
        doc.services = services;
        doc.updated_at = e.ledger().timestamp();

        e.storage().persistent().set(&DataKey::DID(did.clone()), &doc);

        emit_did_updated(e, &did, doc.updated_at);
    }

    /// Deactivate a DID (tombstone). Resolution fails after deactivation.
    /// Only the registered controller may deactivate.
    pub fn deactivate_did(e: &Env, did: String) {
        Self::require_not_paused(e);
        let mut doc = Self::get_did_document(e, &did);
        doc.controller.require_auth();

        if doc.deactivated {
            panic_with_error!(e, DIDRegistryError::DIDDeactivated);
        }

        doc.deactivated = true;
        doc.updated_at = e.ledger().timestamp();

        e.storage().persistent().set(&DataKey::DID(did.clone()), &doc);

        emit_did_deactivated(e, &did, doc.updated_at);
    }

    /// Check whether a DID is registered (regardless of deactivation status).
    pub fn did_exists(e: &Env, did: String) -> bool {
        e.storage().persistent().has(&DataKey::DID(did))
    }

    /// List all DIDs controlled by an address.
    pub fn get_controller_dids(e: &Env, controller: Address) -> Vec<String> {
        e.storage()
            .persistent()
            .get(&DataKey::ControllerDIDs(controller))
            .unwrap_or(Vec::new(e))
    }

    /// Total number of registered DIDs.
    pub fn get_did_count(e: &Env) -> u64 {
        e.storage().instance().get(&DataKey::DIDCount).unwrap_or(0)
    }

    // ─── Credential Status ────────────────────

    /// Anchor a verifiable credential hash on-chain with Valid status.
    /// Caller must be the controller of the issuer DID.
    ///
    /// # Arguments
    /// * `vc_hash` - SHA-256 hash of the canonical VC JSON
    /// * `issuer_did` - DID of the credential issuer (must be registered)
    /// * `subject_did` - DID of the credential subject
    /// * `vc_type` - Credential type (e.g. NationalIdentityCredential)
    pub fn anchor_credential(
        e: &Env,
        vc_hash: BytesN<32>,
        issuer_did: String,
        subject_did: String,
        vc_type: Symbol,
    ) {
        Self::require_not_paused(e);
        // Issuer DID must be registered and caller must be its controller
        let issuer_doc = Self::get_did_document(e, &issuer_did);
        issuer_doc.controller.require_auth();

        if issuer_doc.deactivated {
            panic_with_error!(e, DIDRegistryError::DIDDeactivated);
        }

        // Reject zero hash
        let zero_hash = BytesN::from_array(e, &[0; 32]);
        if vc_hash == zero_hash {
            panic_with_error!(e, DIDRegistryError::InvalidHash);
        }

        let key = DataKey::Credential(vc_hash.clone());
        if e.storage().persistent().has(&key) {
            panic_with_error!(e, DIDRegistryError::CredentialAlreadyAnchored);
        }

        let record = CredentialRecord {
            vc_hash: vc_hash.clone(),
            issuer_did: issuer_did.clone(),
            subject_did,
            vc_type,
            status: CredentialStatus::Valid,
            anchored_at: e.ledger().timestamp(),
            revoked_at: None,
        };

        e.storage().persistent().set(&key, &record);

        emit_vc_anchored(e, &vc_hash, &issuer_did, record.anchored_at);
    }

    /// Revoke an anchored credential. Only the issuer DID's controller may revoke.
    pub fn revoke_credential(e: &Env, vc_hash: BytesN<32>) {
        Self::require_not_paused(e);
        let key = DataKey::Credential(vc_hash.clone());
        let mut record: CredentialRecord = e
            .storage()
            .persistent()
            .get(&key)
            .unwrap_or_else(|| panic_with_error!(e, DIDRegistryError::CredentialNotFound));

        // Only the issuer DID's controller may revoke
        let issuer_doc = Self::get_did_document(e, &record.issuer_did);
        issuer_doc.controller.require_auth();

        if record.status == CredentialStatus::Revoked {
            panic_with_error!(e, DIDRegistryError::AlreadyRevoked);
        }

        record.status = CredentialStatus::Revoked;
        record.revoked_at = Some(e.ledger().timestamp());

        e.storage().persistent().set(&key, &record);

        emit_vc_revoked(e, &vc_hash, e.ledger().timestamp());
    }

    /// Get the status record for an anchored credential.
    /// Panics with CredentialNotFound if the hash was never anchored.
    pub fn credential_status(e: &Env, vc_hash: BytesN<32>) -> CredentialRecord {
        e.storage()
            .persistent()
            .get(&DataKey::Credential(vc_hash))
            .unwrap_or_else(|| panic_with_error!(e, DIDRegistryError::CredentialNotFound))
    }

    // ─── Private Helpers ──────────────────────

    /// Shared registration logic used by register_did and register_soroban_did.
    /// Caller is responsible for auth and input validation.
    fn do_register(
        e: &Env,
        did: String,
        method: DIDMethod,
        controller: Address,
        verification_methods: Vec<VerificationMethodEntry>,
        services: Vec<ServiceEntry>,
    ) {
        let key = DataKey::DID(did.clone());
        if e.storage().persistent().has(&key) {
            panic_with_error!(e, DIDRegistryError::DIDAlreadyExists);
        }

        let now = e.ledger().timestamp();

        let doc = DIDDocumentData {
            did: did.clone(),
            method,
            controller: controller.clone(),
            verification_methods,
            services,
            created_at: now,
            updated_at: now,
            deactivated: false,
        };

        e.storage().persistent().set(&key, &doc);

        // Index by controller
        let ctrl_key = DataKey::ControllerDIDs(controller.clone());
        let mut dids: Vec<String> = e.storage().persistent().get(&ctrl_key).unwrap_or(Vec::new(e));
        dids.push_back(did.clone());
        e.storage().persistent().set(&ctrl_key, &dids);

        let count: u64 = e.storage().instance().get(&DataKey::DIDCount).unwrap_or(0);
        e.storage().instance().set(&DataKey::DIDCount, &(count + 1));

        emit_did_registered(e, &did, &controller, now);
    }

    fn get_did_document(e: &Env, did: &String) -> DIDDocumentData {
        e.storage()
            .persistent()
            .get(&DataKey::DID(did.clone()))
            .unwrap_or_else(|| panic_with_error!(e, DIDRegistryError::DIDNotFound))
    }

    /// Require the caller to be the contract owner (replaces only_owner).
    fn require_owner(e: &Env) {
        Self::owner(e).require_auth();
    }

    fn require_not_paused(e: &Env) {
        if Self::is_paused(e) {
            panic_with_error!(e, DIDRegistryError::ContractPaused);
        }
    }

    fn validate_did(e: &Env, did: &String) {
        let len = did.len();
        if len == 0 || len > 200 {
            panic_with_error!(e, DIDRegistryError::InvalidDID);
        }
        // Minimal DID syntax check: must start with "did:"
        let bytes = did.to_bytes();
        if bytes.len() < 4 {
            panic_with_error!(e, DIDRegistryError::InvalidDID);
        }
        let prefix = bytes.slice(0..4);
        let expected = soroban_sdk::Bytes::from_slice(e, b"did:");
        if prefix != expected {
            panic_with_error!(e, DIDRegistryError::InvalidDID);
        }
    }
}

#[cfg(test)]
mod test;
