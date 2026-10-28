import CryptoKit
import Foundation
import LocalAuthentication
import Security

// Status codes shared with src/approver.rs.
let okStatus: Int32 = 0
let unavailable: Int32 = 1
let failed: Int32 = 2
let cancelled: Int32 = 3
let tooSmall: Int32 = 4

func copyOut(_ data: Data, _ out: UnsafeMutablePointer<UInt8>, _ cap: Int, _ len: UnsafeMutablePointer<Int>) -> Int32 {
    len.pointee = data.count
    if data.count > cap { return tooSmall }
    data.copyBytes(to: out, count: data.count)
    return okStatus
}

/// Creates a P-256 key in the Secure Enclave. The private key never leaves it: `blob` is its
/// dataRepresentation, which only this Mac's Secure Enclave can use. With biometry set, every
/// signature needs Touch ID with the fingers enrolled today.
@_cdecl("th_se_create")
public func thSECreate(_ biometry: Bool, _ blob: UnsafeMutablePointer<UInt8>, _ blobCap: Int, _ blobLen: UnsafeMutablePointer<Int>,
                       _ pub: UnsafeMutablePointer<UInt8>, _ pubCap: Int, _ pubLen: UnsafeMutablePointer<Int>) -> Int32 {
    guard SecureEnclave.isAvailable else { return unavailable }
    var flags: SecAccessControlCreateFlags = [.privateKeyUsage]
    if biometry { flags.insert(.biometryCurrentSet) }
    guard let ac = SecAccessControlCreateWithFlags(nil, kSecAttrAccessibleWhenUnlockedThisDeviceOnly, flags, nil) else { return failed }
    do {
        let key = try SecureEnclave.P256.Signing.PrivateKey(compactRepresentable: false, accessControl: ac)
        let st = copyOut(key.dataRepresentation, blob, blobCap, blobLen)
        if st != okStatus { return st }
        return copyOut(key.publicKey.x963Representation, pub, pubCap, pubLen)
    } catch {
        return failed
    }
}

@_cdecl("th_se_public")
public func thSEPublic(_ blob: UnsafePointer<UInt8>, _ blobLen: Int, _ pub: UnsafeMutablePointer<UInt8>, _ pubCap: Int, _ pubLen: UnsafeMutablePointer<Int>) -> Int32 {
    guard SecureEnclave.isAvailable else { return unavailable }
    do {
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: Data(bytes: blob, count: blobLen))
        return copyOut(key.publicKey.x963Representation, pub, pubCap, pubLen)
    } catch {
        return failed
    }
}

/// Signs msg (ECDSA P-256 over SHA-256, DER). Touch ID shows `reason`.
@_cdecl("th_se_sign")
public func thSESign(_ blob: UnsafePointer<UInt8>, _ blobLen: Int, _ msg: UnsafePointer<UInt8>, _ msgLen: Int, _ reason: UnsafePointer<CChar>,
                     _ sig: UnsafeMutablePointer<UInt8>, _ sigCap: Int, _ sigLen: UnsafeMutablePointer<Int>) -> Int32 {
    guard SecureEnclave.isAvailable else { return unavailable }
    let ctx = LAContext()
    ctx.localizedReason = String(cString: reason)
    do {
        let key = try SecureEnclave.P256.Signing.PrivateKey(dataRepresentation: Data(bytes: blob, count: blobLen), authenticationContext: ctx)
        let s = try key.signature(for: Data(bytes: msg, count: msgLen))
        return copyOut(s.derRepresentation, sig, sigCap, sigLen)
    } catch let e as LAError where e.code == .userCancel || e.code == .appCancel || e.code == .systemCancel {
        return cancelled
    } catch {
        return failed
    }
}
