"""Registers a throwaway account, doing what the web vault does in the browser.

The server never sees the password, so registering means deriving the keys
locally. Credentials are generated per run and never printed.
"""
import base64
import hashlib
import hmac
import json
import os
import secrets
import ssl
import urllib.error
import urllib.request

from cryptography.hazmat.primitives import padding, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

ITERATIONS = 600_000


def _b64(data):
    return base64.b64encode(data).decode()


def _expand(prk, info):
    return hmac.new(prk, info + b"\x01", hashlib.sha256).digest()


def _enc_string(plain, enc_key, mac_key):
    iv = os.urandom(16)
    padder = padding.PKCS7(128).padder()
    data = padder.update(plain) + padder.finalize()
    enc = Cipher(algorithms.AES(enc_key), modes.CBC(iv)).encryptor()
    ct = enc.update(data) + enc.finalize()
    mac = hmac.new(mac_key, iv + ct, hashlib.sha256).digest()
    return f"2.{_b64(iv)}|{_b64(ct)}|{_b64(mac)}"


def _post(base, path, payload):
    # The test talks to a server it just created, so this is not a trust decision.
    context = ssl._create_unverified_context()
    req = urllib.request.Request(base + path, json.dumps(payload).encode(),
                                 {"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30, context=context) as resp:
            return resp.read().decode()
    except urllib.error.HTTPError as err:
        raise RuntimeError(f"{path}: {err.code}") from None


def register(base, email="e2e-test@example.test"):
    password = secrets.token_urlsafe(24)
    master = hashlib.pbkdf2_hmac("sha256", password.encode(), email.encode(), ITERATIONS, 32)
    master_hash = _b64(hashlib.pbkdf2_hmac("sha256", master, password.encode(), 1, 32))
    user_key = os.urandom(64)
    protected = _enc_string(user_key, _expand(master, b"enc"), _expand(master, b"mac"))
    rsa_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    private = rsa_key.private_bytes(serialization.Encoding.DER, serialization.PrivateFormat.PKCS8,
                                    serialization.NoEncryption())
    public = rsa_key.public_key().public_bytes(serialization.Encoding.DER,
                                               serialization.PublicFormat.SubjectPublicKeyInfo)
    token = _post(base, "/identity/accounts/register/send-verification-email",
                  {"email": email, "name": "e2e", "receiveMarketingEmails": False})
    _post(base, "/identity/accounts/register/finish", {
        "email": email, "name": "e2e",
        "emailVerificationToken": token.strip().strip('"') or None,
        "masterPasswordHash": master_hash, "masterPasswordHint": None,
        "userSymmetricKey": protected,
        "userAsymmetricKeys": {
            "publicKey": _b64(public),
            "encryptedPrivateKey": _enc_string(private, user_key[:32], user_key[32:]),
        },
        "kdf": 0, "kdfIterations": ITERATIONS,
    })
    return {"email": email, "password": password}
