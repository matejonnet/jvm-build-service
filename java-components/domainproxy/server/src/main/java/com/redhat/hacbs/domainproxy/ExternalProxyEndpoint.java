package com.redhat.hacbs.domainproxy;

import java.io.InputStream;

import jakarta.enterprise.context.ApplicationScoped;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.NotFoundException;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.client.Client;
import jakarta.ws.rs.core.Context;
import jakarta.ws.rs.core.UriInfo;

import org.jboss.resteasy.reactive.client.impl.ClientBuilderImpl;

import io.quarkus.logging.Log;

@Path("/")
@ApplicationScoped
public class ExternalProxyEndpoint {

    final Client client;

    public ExternalProxyEndpoint() {
        ClientBuilderImpl clientBuilder = new ClientBuilderImpl();
        //        clientBuilder.proxy("indy", 8080);
        //        clientBuilder.proxyUser("user");
        //        clientBuilder.proxyPassword("pass");
        //        clientBuilder.nonProxyHosts("indy-mvn");
        client = clientBuilder.build();
    }

    @Context
    UriInfo uri;

    @GET
    public InputStream get() {
        var response = client.target(uri.getBaseUri()).request().get();
        if (response.getStatus() != 200) {
            Log.errorf("Response %d %s", response.getStatus(), response.readEntity(String.class));
            throw new NotFoundException();
        }
        return response.readEntity(InputStream.class);
    }
}
