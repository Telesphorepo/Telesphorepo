package com.telesphorepo.maven.client;

import java.io.IOException;

/** A Telesphorepo API call answered with an error status. */
public final class ApiException extends IOException {
    private final int status;
    private final String error;

    public ApiException(String what, int status, String error) {
        super(what + ": HTTP " + status + (error == null || error.isEmpty() ? "" : ": " + error));
        this.status = status;
        this.error = error == null ? "" : error;
    }

    /** The HTTP status. */
    public int status() {
        return status;
    }

    /** The "error" field of the answer, or an empty string. */
    public String error() {
        return error;
    }
}
