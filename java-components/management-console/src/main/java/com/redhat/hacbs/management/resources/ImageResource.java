package com.redhat.hacbs.management.resources;

import java.nio.charset.StandardCharsets;
import java.util.Base64;
import java.util.List;

import jakarta.enterprise.inject.Instance;
import jakarta.inject.Inject;
import jakarta.ws.rs.GET;
import jakarta.ws.rs.NotFoundException;
import jakarta.ws.rs.Path;
import jakarta.ws.rs.PathParam;
import jakarta.ws.rs.QueryParam;

import com.redhat.hacbs.management.dto.ImageDTO;
import com.redhat.hacbs.management.dto.PageParameters;
import com.redhat.hacbs.management.model.ContainerImage;

import io.fabric8.kubernetes.client.KubernetesClient;
import io.quarkus.panache.common.Page;
import io.quarkus.panache.common.Sort;

@Path("/image")
public class ImageResource {
    @Inject
    Instance<KubernetesClient> kubernetesClient;

    @GET
    @Path("{repository}")
    public PageParameters<ImageDTO> getImages(@PathParam("repository") String repository, @QueryParam("page") int page,
            @QueryParam("perPage") int perPage) {
        if (perPage <= 0) {
            perPage = 20;
        }
        List<ContainerImage> all = ContainerImage
                .find("repository.repository", Sort.descending("timestamp"),
                        new String(Base64.getUrlDecoder().decode(repository), StandardCharsets.UTF_8))
                .page(Page.of(page - 1, perPage)).list();
        return new PageParameters<>(
                all.stream().map(ImageDTO::of)
                        .toList(),
                ContainerImage.count(), page, perPage);
    }

    @GET
    @Path("scan/{image}")
    public ImageDTO getImage(@PathParam("image") String image) {
        ContainerImage im = ContainerImage.get(image);
        if (im == null) {
            throw new NotFoundException();
        }
        return ImageDTO.of(im);
    }
}
