package com.redhat.hacbs.recipies.location;

import java.nio.file.Path;
import java.time.Duration;
import java.util.Optional;

import org.eclipse.jgit.api.Git;
import org.eclipse.jgit.api.errors.GitAPIException;

import com.redhat.hacbs.recipies.util.GitCredentials;

/**
 * A recipe database stored in git.
 */
public class RecipeRepositoryManager implements RecipeDirectory {
    public static final String SCM_INFO = "scm-info";
    public static final String BUILD_INFO = "build-info";
    public static final String REPOSITORY_INFO = "repository-info";
    private final Git git;
    private final String remote;
    private final Path local;
    private final String branch;
    private final Optional<Duration> updateInterval;
    private final RecipeLayoutManager recipeLayoutManager;
    private volatile long lastUpdate = -1;

    public RecipeRepositoryManager(Git git, String remote, Path local, String branch, Optional<Duration> updateInterval) {
        this.git = git;
        this.remote = remote;
        this.local = local;
        this.branch = branch;
        this.updateInterval = updateInterval;
        this.lastUpdate = System.currentTimeMillis();
        this.recipeLayoutManager = new RecipeLayoutManager(local);
    }

    public static RecipeRepositoryManager create(String remote, String branch, Optional<Duration> updateInterval,
            Path directory) throws GitAPIException {
        var clone = Git.cloneRepository()
                .setBranch(branch)
                .setDirectory(directory.toFile())
                .setCredentialsProvider(new GitCredentials())
                .setURI(remote);
        var result = clone.call();

        return new RecipeRepositoryManager(result, remote, directory, branch, updateInterval);
    }

    /**
     * Returns the directories that contain the recipe information for this specific artifact
     *
     * @param groupId The group id
     * @param artifactId The artifact id
     * @param version The version
     * @return The path match result
     */
    public Optional<RecipePathMatch> getArtifactPaths(String groupId, String artifactId, String version) {
        doUpdate();
        return recipeLayoutManager.getArtifactPaths(groupId, artifactId, version);
    }

    @Override
    public Optional<Path> getBuildPaths(String scmUri, String version) {
        doUpdate();
        return recipeLayoutManager.getBuildPaths(scmUri, version);
    }

    @Override
    public Optional<Path> getRepositoryPaths(String name) {
        doUpdate();
        return recipeLayoutManager.getRepositoryPaths(name);
    }

    private void doUpdate() {
        if (updateInterval.isEmpty()) {
            return;
        }
        if (lastUpdate + updateInterval.get().toMillis() < System.currentTimeMillis()) {
            synchronized (this) {
                if (lastUpdate + updateInterval.get().toMillis() < System.currentTimeMillis()) {
                    try {
                        git.pull().call();
                    } catch (GitAPIException e) {
                        throw new RuntimeException(e);
                    }
                    lastUpdate = System.currentTimeMillis();
                }
            }
        }
    }

    @Override
    public String toString() {
        return "RecipeRepositoryManager{" +
                ", remote='" + remote + '\'' +
                ", local=" + local +
                ", branch='" + branch + '\'' +
                ", updateInterval=" + updateInterval +
                ", recipeLayoutManager=" + recipeLayoutManager +
                ", lastUpdate=" + lastUpdate +
                '}';
    }
}
